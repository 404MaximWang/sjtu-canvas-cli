// Package mlearning implements the mlearning.sjtu.edu.cn attendance
// endpoints: the SSO chain that plants the domain's Access_Token cookie,
// the three read projections behind /courses/<id>/attendance, and the
// scan-submit write of `sjtu attendance submit`.
//
// Token model: a JAAuthCookie-authenticated visit to any forscan URL rides
// the SSO chain and ends with the callback planting a `token` cookie in the
// mlearning domain. That cookie is the student client's Access_Token and
// authenticates API requests as a bare Authorization header value, without
// a Bearer prefix (verified against the live service on 2026-10-01).
//
// A dead Access_Token is rejected in the business layer, never as 401: the
// API answers HTTP 200 carrying {"resultCode":"10001"} ("token不存在，请
// 重新登录！"). Reads detect that shape, clear the planted token, re-run
// the SSO chain, and retry once; a persistent rejection passes through
// verbatim for the caller to judge.
package mlearning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

const (
	// baseURL is the mlearning host every endpoint lives on.
	baseURL = "https://mlearning.sjtu.edu.cn"
	// forscanPath prefixes the QR landing URLs; a JAAuthCookie-carrying
	// visit to one rides the SSO chain and plants the token cookie.
	forscanPath = "/lms/mobile2/forscan/"
	// jaccountHost answers instead of mlearning when the SSO cookie expired.
	jaccountHost = "jaccount.sjtu.edu.cn"
	// jaAuthCookieName is the jAccount SSO credential cookie: the one cookie
	// a clean-slate SSO chain keeps.
	jaAuthCookieName = "JAAuthCookie"
)

// ErrAuth reports an expired or missing jAccount session: the SSO chain
// bounced back to the login host instead of reaching mlearning.
var ErrAuth = errors.New("jaccount authentication required; run `sjtu auth jaccount login`")

// ErrInvalidURL rejects a scanned QR URL that is not a well-formed forscan
// link; the CLI maps it to a usage error.
var ErrInvalidURL = errors.New("not an mlearning forscan URL")

// Client talks to mlearning over a cookie session holding a valid
// JAAuthCookie.
type Client struct {
	sess *session.Session
	base string // API origin; a field so tests can point at a stub server
	mu   sync.Mutex
}

// New builds a Client over the given cookie session; the session must carry
// a JAAuthCookie for the jAccount domain family.
func New(sess *session.Session) *Client {
	return &Client{sess: sess, base: baseURL}
}

// Status reports whether the course has a roll call in progress, as the raw
// existentB response (idle state: {"resultCode":"200","body":{}}).
func (c *Client) Status(ctx context.Context, courseID int64) (json.RawMessage, error) {
	return c.get(ctx, "/lms-lti-rollcall-sjtu/rollcall/existentB?courseCode="+strconv.FormatInt(courseID, 10))
}

// Current reports whether the course is in session right now, as the raw
// barrage2 current response (an empty curricula array means no class).
func (c *Client) Current(ctx context.Context, courseID int64) (json.RawMessage, error) {
	return c.get(ctx, "/lms-interactive_barrage2/api/jwb/current?cids[]="+strconv.FormatInt(courseID, 10))
}

// Records lists the caller's sign-in records of the course, as the raw
// sign/records response with the UI's own paging parameters.
func (c *Client) Records(ctx context.Context, courseID int64) (json.RawMessage, error) {
	return c.get(ctx, "/lms-lti-rollcall-sjtu/sign/records?courseCode="+strconv.FormatInt(courseID, 10)+
		"&pageNum=1&pageSize=500")
}

// Self fetches the caller's identity as the raw users/self response. It is
// the mlearning domain's lightweight health probe: a live token answers
// 200 with the identity body, a dead one the 10001 rejection.
func (c *Client) Self(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, "/lms-canvas-sjtu/users/self")
}

// Submit signs one roll call given its scanned QR URL: the URL itself rides
// the SSO chain, then the scan endpoint is called. The upstream response
// (resultCode/body.status/message) passes through verbatim; business
// outcomes, including SIGNED, are the caller's to judge.
func (c *Client) Submit(ctx context.Context, rawURL string) (json.RawMessage, error) {
	rollCallToken, signHistoryID, err := ValidateForscanURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := c.sso(ctx, rawURL); err != nil {
		return nil, err
	}
	token, err := c.plantedToken()
	if err != nil {
		return nil, err
	}
	header := http.Header{
		"Authorization":    {token},
		"X-Requested-With": {"XMLHttpRequest"},
		"Referer":          {c.base + "/lms/mobile/"},
	}
	scanURL := c.base + "/lms-lti-rollcall-sjtu/sign/scan/" +
		url.PathEscape(rollCallToken) + "/" + url.PathEscape(signHistoryID)
	var raw json.RawMessage
	if err := c.sess.DoJSONWith(ctx, http.MethodGet, scanURL, header, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// get runs one authenticated GET against the API and returns the response
// body verbatim: read projections pass upstream JSON through untouched. On
// the business-layer token rejection (HTTP 200, resultCode 10001) the
// domain's cookies are cleared, the SSO chain re-run, and the request
// retried once; a persistent rejection passes through like any other
// response.
func (c *Client) get(ctx context.Context, path string) (json.RawMessage, error) {
	raw, err := c.getOnce(ctx, path)
	if err != nil || !tokenRejected(raw) {
		return raw, err
	}
	slog.Info("mlearning access token rejected, re-running SSO chain")
	c.sess.ClearCookies(c.base+"/", jaAuthCookieName)
	return c.getOnce(ctx, path)
}

// getOnce performs one authenticated GET attempt with the current token.
func (c *Client) getOnce(ctx context.Context, path string) (json.RawMessage, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	header := http.Header{"Authorization": {token}}
	if err := c.sess.DoJSONWith(ctx, http.MethodGet, c.base+path, header, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// tokenRejected reports whether raw is the business-layer rejection of a
// dead Access_Token: {"resultCode":"10001"}. The shape is pinned to the
// observed response — a missing or non-string resultCode is not it.
func tokenRejected(raw json.RawMessage) bool {
	var envelope struct {
		ResultCode string `json:"resultCode"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	return envelope.ResultCode == "10001"
}

// token returns the mlearning Access_Token, running the SSO chain on first
// use and after every rejection-driven clear. A fabricated forscan URL triggers the
// chain (verified: any well-formed forscan URL plants the cookie, whatever
// its parameters); the jar then carries the token for every later call.
// The double-checked lock keeps concurrent daemon requests from stampeding
// the chain: the fast path is lock-free, and a request that queued on the
// lock re-checks the jar before running its own chain.
func (c *Client) token(ctx context.Context) (string, error) {
	if token, ok := c.sess.Cookie(c.base+"/", "token"); ok {
		return cleanToken(token)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if token, ok := c.sess.Cookie(c.base+"/", "token"); ok {
		return cleanToken(token)
	}
	if err := c.sso(ctx, c.base+forscanPath+"?rollCallToken=0&signHistoryId=0"); err != nil {
		return "", err
	}
	return c.plantedToken()
}

// sso visits one mlearning URL with redirect following, running the SSO
// chain that plants the token cookie as a side effect. The chain starts
// from a clean slate: cookies left over from an earlier chain (the token's
// session companions) make mlearning answer the forscan entry with an
// immediate 500 instead of opening the jAccount redirect chain, which
// permanently broke re-authentication until restart. The JAAuthCookie is
// kept — it is the credential the chain authenticates with.
//
// The terminal forscan page 500s as a matter of course (the QR parameters
// may be fabricated), but the callback plants the token cookie before that
// page loads — so an HTTP error late in the chain is tolerated when the jar
// proves the cookie landed. A dead JAAuthCookie still surfaces as ErrAuth:
// its chain ends on the login page, which answers 200.
func (c *Client) sso(ctx context.Context, rawURL string) error {
	c.sess.ClearCookies(c.base+"/", jaAuthCookieName)
	_, finalURL, err := c.sess.GetHTML(ctx, rawURL)
	if err != nil {
		var httpErr *session.HTTPError
		if errors.As(err, &httpErr) {
			if _, ok := c.sess.Cookie(c.base+"/", "token"); ok {
				return nil
			}
		}
		return err
	}
	if u, err := url.Parse(finalURL); err == nil && u.Host == jaccountHost {
		return ErrAuth
	}
	return nil
}

// plantedToken reads the token cookie the SSO chain must have planted.
func (c *Client) plantedToken() (string, error) {
	token, ok := c.sess.Cookie(c.base+"/", "token")
	if !ok {
		return "", errors.New("mlearning SSO chain completed but planted no token cookie")
	}
	return cleanToken(token)
}

// cleanToken normalizes the raw cookie value (URL-unquote, then strip
// surrounding quotes) and registers it with the log redactor.
func cleanToken(raw string) (string, error) {
	unquoted, err := url.QueryUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("malformed token cookie: %w", err)
	}
	token := strings.Trim(unquoted, `"`)
	if token == "" {
		return "", errors.New("empty token cookie")
	}
	session.RegisterSecret(token)
	return token, nil
}

// ValidateForscanURL checks a scanned QR URL and extracts its roll-call
// parameters: https scheme, mlearning host, the forscan path prefix, and
// non-empty rollCallToken/signHistoryId query parameters.
func ValidateForscanURL(rawURL string) (rollCallToken, signHistoryID string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "mlearning.sjtu.edu.cn") ||
		!strings.HasPrefix(u.Path, forscanPath) {
		return "", "", ErrInvalidURL
	}
	q := u.Query()
	rollCallToken, signHistoryID = q.Get("rollCallToken"), q.Get("signHistoryId")
	if rollCallToken == "" || signHistoryID == "" {
		return "", "", fmt.Errorf("%w: rollCallToken and signHistoryId must both be non-empty", ErrInvalidURL)
	}
	return rollCallToken, signHistoryID, nil
}
