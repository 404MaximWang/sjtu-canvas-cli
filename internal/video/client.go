// Package video implements the v.sjtu.edu.cn course-video API: the LTI
// launch chain that yields a per-course jwt-token, the replay and live
// listings, and the ranged media download path.
//
// Authentication chain:
//
//  1. GET oc.sjtu.edu.cn/courses/{id}/external_tools/8329 with the Canvas
//     cookie session (JAAuthCookie rides the SSO redirects).
//  2. Parse the auto-submitting form, POST it; the answer carries a second
//     auto-submitting form.
//  3. POST that form WITHOUT following redirects; the 302 Location carries
//     jwt_token in its fragment or query.
//  4. GET v.sjtu.edu.cn/jy-application-resourcemanage/lms/launch-context
//     with the jwt-token header to learn the teachingClassId.
//
// The jwt-token is never cached: every API operation re-runs the chain,
// matching the blueprint. All API requests carry the jwt-token header and a
// fixed Referer of the resource-manage UI.
package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

const (
	// canvasBaseURL hosts the LTI launch endpoint.
	canvasBaseURL = "https://oc.sjtu.edu.cn"
	// externalToolID is the Canvas external-tool id of the video platform.
	externalToolID = 8329
	// resourceManageBaseURL is the v.sjtu.edu.cn API root.
	resourceManageBaseURL = "https://v.sjtu.edu.cn/jy-application-resourcemanage"
	// resourceManageUIURL is the fixed Referer the API expects.
	resourceManageUIURL = "https://v.sjtu.edu.cn/jy-application-resourcemanage-ui/"
	// jaccountHost answers instead of Canvas when the SSO cookie expired.
	jaccountHost = "jaccount.sjtu.edu.cn"
)

// ErrAuth reports an expired or missing jAccount session: the SSO chain
// bounced back to the login host instead of reaching Canvas.
var ErrAuth = errors.New("jaccount authentication required; run `sjtu auth jaccount login`")

// Client talks to the video platform over a cookie session holding a valid
// JAAuthCookie. Media bytes are fetched through a separate bare client: the
// signed URLs authenticate themselves and must not see session cookies.
type Client struct {
	sess  *session.Session
	media *http.Client

	mu       sync.Mutex
	warmedUp bool // Canvas web session established via openid_connect
}

// New builds a Client over the given cookie session; the session must carry
// a JAAuthCookie for the jAccount domain family.
func New(sess *session.Session) *Client {
	return &Client{sess: sess, media: &http.Client{}}
}

// launchState is the per-course product of the launch chain.
type launchState struct {
	token           string
	teachingClassID int64
}

// apiHeaders are the headers every video API request carries.
func (l *launchState) apiHeaders() http.Header {
	return http.Header{
		"jwt-token": {l.token},
		"Referer":   {resourceManageUIURL},
	}
}

// canvasLoginURL establishes the Canvas web session through jAccount SSO.
const canvasLoginURL = canvasBaseURL + "/login/openid_connect"

// launch runs the full chain for one course. It is intentionally called by
// every public API method: the jwt-token is single-purpose and short-lived.
func (c *Client) launch(ctx context.Context, courseID int64) (*launchState, error) {
	if err := c.warmup(ctx); err != nil {
		return nil, err
	}
	toolURL := fmt.Sprintf("%s/courses/%d/external_tools/%d", canvasBaseURL, courseID, externalToolID)
	body, finalURL, err := c.sess.GetHTML(ctx, toolURL)
	if err != nil {
		return nil, err
	}
	if err := checkNotLoginRedirect(finalURL); err != nil {
		return nil, err
	}
	action, form, err := parseLaunchForm(body)
	if err != nil {
		return nil, fmt.Errorf("launch form (first level): %w", err)
	}
	action, err = resolveURL(finalURL, action)
	if err != nil {
		return nil, err
	}

	body, finalURL, err = c.sess.PostFormHTML(ctx, action, form)
	if err != nil {
		return nil, err
	}
	if err := checkNotLoginRedirect(finalURL); err != nil {
		return nil, err
	}
	action, form, err = parseLaunchForm(body)
	if err != nil {
		return nil, fmt.Errorf("launch form (second level): %w", err)
	}
	action, err = resolveURL(finalURL, action)
	if err != nil {
		return nil, err
	}

	location, err := c.sess.PostFormRedirect(ctx, action, form)
	if err != nil {
		return nil, err
	}
	location, err = resolveURL(action, location)
	if err != nil {
		return nil, err
	}
	token, err := jwtFromLocation(location)
	if err != nil {
		return nil, err
	}
	session.RegisterSecret(token)

	l := &launchState{token: token}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/lms/launch-context", l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	data, err := apiData(raw)
	if err != nil {
		return nil, err
	}
	record, _ := data["canvasRecord"].(map[string]any)
	id, ok := valueAsInt64(record["teachingClassId"])
	if !ok {
		return nil, errors.New("launch-context: teachingClassId missing")
	}
	l.teachingClassID = id
	return l, nil
}

// warmup establishes the Canvas web session once per client: the
// openid_connect endpoint rides the jAccount SSO redirect and plants the
// Canvas cookies; without it the external_tools page answers with the
// login form, whose POST target 400s.
func (c *Client) warmup(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.warmedUp {
		return nil
	}
	_, finalURL, err := c.sess.GetHTML(ctx, canvasLoginURL)
	if err != nil {
		return err
	}
	if err := checkNotLoginRedirect(finalURL); err != nil {
		return err
	}
	c.warmedUp = true
	return nil
}

// checkNotLoginRedirect reports ErrAuth when the SSO chain ended on the
// jAccount login host instead of the target service.
func checkNotLoginRedirect(finalURL string) error {
	u, err := url.Parse(finalURL)
	if err == nil && u.Host == jaccountHost {
		return ErrAuth
	}
	return nil
}

// resolveURL resolves ref against base, both of which may be relative in
// either position.
func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base URL %q: %w", base, err)
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", ref, err)
	}
	return b.ResolveReference(r).String(), nil
}

// Replay is one recorded lecture in the vod list: raw upstream fields only.
// Display naming is the presentation layer's job.
type Replay struct {
	ID          int64  // video id in the vod system ("id" upstream, always a JSON number)
	SubjName    string // course name as upstream reports it ("subjName")
	Teacher     string
	Classroom   string
	BeginTime   time.Time
	EndTime     time.Time // zero when upstream omits courEndTime
	WeekNumber  int64
	WeekDay     int64 // 1 = Monday; 0 when upstream omits it
	DailyLesson int64 // Nth lecture of that calendar day, 1-based
	VodStatus   int64
	Status      string // ready | repairing | unavailable
}

// Playable reports whether the replay's media URLs can be fetched.
func (r Replay) Playable() bool { return r.VodStatus == 5 }

// Replays lists every replay of one course, all vodStatus values included.
func (c *Client) Replays(ctx context.Context, courseID int64) ([]Replay, error) {
	l, err := c.launch(ctx, courseID)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"page.pageIndex":       {"1"},
		"page.pageSize":        {"1000"},
		"teclIds":              {strconv.FormatInt(l.teachingClassID, 10)},
		"page.orders[0].asc":   {"false"},
		"page.orders[0].field": {"courBeginTime"},
		"schoolOpenStatusFlag": {"false"},
	}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/v1/subject_vod_list_new?"+q.Encode(), l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	return parseReplays(raw)
}

// View is one camera angle of a replay; ViewNum is the server-assigned
// number and is NOT contiguous (1 and 5 observed).
type View struct {
	ViewNum int64
	URL     string
}

// ReplayViews fetches the per-view media URLs of one replay.
func (c *Client) ReplayViews(ctx context.Context, courseID int64, replayID int64) ([]View, error) {
	l, err := c.launch(ctx, courseID)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/v1/course_vod_urls_new?courseId="+strconv.FormatInt(replayID, 10),
		l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	return parseViews(raw)
}

// Subtitle fetches the machine transcript of one replay as the raw
// afterAssemblyList JSON array ([{bg,ed,res}], millisecond offsets).
func (c *Client) Subtitle(ctx context.Context, courseID int64, replayID int64) (json.RawMessage, error) {
	l, err := c.launch(ctx, courseID)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/v1/course/ai/translate/"+strconv.FormatInt(replayID, 10)+"?useOriginal=true",
		l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	data, err := apiData(raw)
	if err != nil {
		return nil, fmt.Errorf("subtitle unavailable: %w", err)
	}
	list, ok := data["afterAssemblyList"]
	if !ok {
		return nil, errors.New("subtitle unavailable")
	}
	out, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	if string(out) == "null" || string(out) == "[]" {
		return nil, errors.New("subtitle unavailable")
	}
	return out, nil
}

// Summary fetches the AI summary of one replay as raw JSON with the
// summary (plain text) and mindmap (knowledge tree) fields of data.
func (c *Client) Summary(ctx context.Context, courseID int64, replayID int64) (json.RawMessage, error) {
	l, err := c.launch(ctx, courseID)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/v1/course/ai/textSummary/"+strconv.FormatInt(replayID, 10),
		l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	data, err := apiData(raw)
	if err != nil {
		return nil, fmt.Errorf("summary unavailable: %w", err)
	}
	out, err := json.Marshal(map[string]any{
		"summary": data["summary"],
		"mindmap": data["mindmap"],
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LiveSession is one currently-broadcasting session of a course.
type LiveSession struct {
	ID          int64
	CourseName  string
	Classroom   string
	LiveEndTime int64 // upstream epoch value, also the auth_key expiry hint
}

// LiveSessions lists the sessions of one course that are broadcasting NOW;
// an empty slice means no live session.
func (c *Client) LiveSessions(ctx context.Context, courseID int64) ([]LiveSession, error) {
	l, err := c.launch(ctx, courseID)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"page.pageIndex":       {"1"},
		"page.pageSize":        {"1000"},
		"page.orders[0].asc":   {"true"},
		"page.orders[0].field": {"courBeginTime"},
		"liveDay":              {"0"},
		"teclId":               {strconv.FormatInt(l.teachingClassID, 10)},
	}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/v1/vod_live/t-1?"+q.Encode(), l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	return parseLiveSessions(raw)
}

// LiveChannel is one playable channel of a live session; URL already
// carries auth_key and account_token.
type LiveChannel struct {
	Name string
	URL  string
}

// LiveChannels lists the channels of one live session.
func (c *Client) LiveChannels(ctx context.Context, courseID, sessionID int64) ([]LiveChannel, error) {
	l, err := c.launch(ctx, courseID)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"courseId": {strconv.FormatInt(sessionID, 10)},
		"playType": {"2"},
	}
	var raw json.RawMessage
	err = c.sess.DoJSONWith(ctx, http.MethodGet,
		resourceManageBaseURL+"/v1/course_vod_videoinfos?"+q.Encode(), l.apiHeaders(), &raw)
	if err != nil {
		return nil, err
	}
	return parseLiveChannels(raw)
}

// Referer is the header set media URLs expect; exposed for the vfs url
// projection.
func Referer() string { return resourceManageUIURL }
