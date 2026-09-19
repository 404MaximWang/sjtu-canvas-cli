// Package session provides authenticated HTTP primitives shared by all SJTU
// services.
//
// A Session couples an http.Client with one identity's credentials. It is
// the only layer that manages response-body lifecycles and credential
// redaction: callers above this package never close a body themselves unless
// they explicitly asked for a stream, and secrets never reach logs.
package session

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// userAgent identifies the CLI as a browser where SJTU portals expect one;
// my.sjtu.edu.cn serves different pages to non-browser agents.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"

// defaultTimeout bounds a whole request when the caller's context carries no
// earlier deadline. Streaming requests must override it via context.
const defaultTimeout = 30 * time.Second

// Session is one identity's authenticated HTTP context. Exactly one of the
// credential fields is active: token sessions call the Canvas API with a
// bearer header and ignore cookies; cookie sessions carry a jar for the
// jAccount SSO domain family.
type Session struct {
	client *http.Client
	jar    *cookiejar.Jar // nil on token sessions
	token  string         // empty on cookie sessions
}

// NewToken builds a Session that authenticates every request with the given
// Canvas bearer token.
func NewToken(token string) *Session {
	return &Session{client: newHTTPClient(nil), token: token}
}

// NewCookies builds a Session with a private, initially empty cookie jar.
// QR login depends on this isolation: a jar shared with an earlier
// authenticated session makes my.sjtu.edu.cn answer with the signed-in
// portal page instead of the login page, and no QR UUID can be extracted.
func NewCookies() (*Session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Session{client: newHTTPClient(jar), jar: jar}, nil
}

// newHTTPClient assembles the shared transport defaults.
func newHTTPClient(jar *cookiejar.Jar) *http.Client {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	// A nil *cookiejar.Jar wrapped in the http.CookieJar interface is NOT a
	// nil interface; assigning it directly makes every request panic inside
	// jar.Cookies. Convert explicitly so token sessions get a true nil Jar.
	var jarIface http.CookieJar
	if jar != nil {
		jarIface = jar
	}
	return &http.Client{Jar: jarIface, Transport: transport, Timeout: defaultTimeout}
}
// JAAuthCookie restored from the credential store. Each domain is passed as
// a bare host ("jaccount.sjtu.edu.cn").
func (s *Session) SeedCookie(name, value string, domains ...string) {
	if s.jar == nil {
		return
	}
	for _, domain := range domains {
		u := &url.URL{Scheme: "https", Host: domain, Path: "/"}
		s.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
	}
}

// Cookie returns the value of a cookie currently held for rawURL, or false.
func (s *Session) Cookie(rawURL, name string) (string, bool) {
	if s.jar == nil {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	for _, c := range s.jar.Cookies(u) {
		if c.Name == name {
			return c.Value, true
		}
	}
	return "", false
}

// Jar exposes the cookie jar for the WebSocket dialer, which needs it to
// attach the login session to its handshake. Nil on token sessions.
func (s *Session) Jar() http.CookieJar {
	if s.jar == nil {
		return nil
	}
	return s.jar
}
