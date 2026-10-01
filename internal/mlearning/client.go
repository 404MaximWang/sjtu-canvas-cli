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
package mlearning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

const (
	// baseURL is the mlearning host every endpoint lives on.
	baseURL = "https://mlearning.sjtu.edu.cn"
	// forscanPath prefixes the QR landing URLs; a JAAuthCookie-carrying
	// visit to one rides the SSO chain and plants the token cookie.
	forscanPath = "/lms/mobile2/forscan/"
	// scanReferer is the Referer the scan endpoint expects.
	scanReferer = baseURL + "/lms/mobile/"
	// jaccountHost answers instead of mlearning when the SSO cookie expired.
	jaccountHost = "jaccount.sjtu.edu.cn"
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
}

// New builds a Client over the given cookie session; the session must carry
// a JAAuthCookie for the jAccount domain family.
func New(sess *session.Session) *Client {
	return &Client{sess: sess}
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
		"Referer":          {scanReferer},
	}
	scanURL := baseURL + "/lms-lti-rollcall-sjtu/sign/scan/" +
		url.PathEscape(rollCallToken) + "/" + url.PathEscape(signHistoryID)
	var raw json.RawMessage
	if err := c.sess.DoJSONWith(ctx, http.MethodGet, scanURL, header, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// get runs one authenticated GET against the API and returns the response
// body verbatim: read projections pass upstream JSON through untouched.
func (c *Client) get(ctx context.Context, path string) (json.RawMessage, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	header := http.Header{"Authorization": {token}}
	if err := c.sess.DoJSONWith(ctx, http.MethodGet, baseURL+path, header, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// token returns the mlearning Access_Token, running the SSO chain on first
// use. A fabricated forscan URL triggers the chain (verified: any
// well-formed forscan URL plants the cookie, whatever its parameters); the
// jar then carries the token for every later call.
func (c *Client) token(ctx context.Context) (string, error) {
	if token, ok := c.sess.Cookie(baseURL+"/", "token"); ok {
		return cleanToken(token)
	}
	if err := c.sso(ctx, baseURL+forscanPath+"?rollCallToken=0&signHistoryId=0"); err != nil {
		return "", err
	}
	return c.plantedToken()
}

// sso visits one mlearning URL with redirect following, running the SSO
// chain that plants the token cookie as a side effect.
func (c *Client) sso(ctx context.Context, rawURL string) error {
	_, finalURL, err := c.sess.GetHTML(ctx, rawURL)
	if err != nil {
		return err
	}
	if u, err := url.Parse(finalURL); err == nil && u.Host == jaccountHost {
		return ErrAuth
	}
	return nil
}

// plantedToken reads the token cookie the SSO chain must have planted.
func (c *Client) plantedToken() (string, error) {
	token, ok := c.sess.Cookie(baseURL+"/", "token")
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
