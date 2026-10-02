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
	"strings"
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
	client     *http.Client
	noRedirect *http.Client   // same transport and jar, never follows redirects
	jar        *cookiejar.Jar // nil on token sessions
	token      string         // empty on cookie sessions
}

// NewToken builds a Session that authenticates every request with the given
// Canvas bearer token.
func NewToken(token string) *Session {
	client, noRedirect := newHTTPClients(nil)
	return &Session{client: client, noRedirect: noRedirect, token: token}
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
	client, noRedirect := newHTTPClients(jar)
	return &Session{client: client, noRedirect: noRedirect, jar: jar}, nil
}

// newHTTPClients assembles the shared transport defaults and builds the
// redirect-following client plus its no-redirect sibling over one transport.
func newHTTPClients(jar *cookiejar.Jar) (*http.Client, *http.Client) {
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
	client := &http.Client{Jar: jarIface, Transport: transport, Timeout: defaultTimeout}
	noRedirect := &http.Client{
		Jar:       jarIface,
		Transport: transport,
		Timeout:   defaultTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client, noRedirect
}

// SeedCookie plants a JAAuthCookie restored from the credential store. Each
// domain is passed as a bare host ("jaccount.sjtu.edu.cn").
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

// ClearCookies expires every cookie that would be sent to rawURL — except
// the names in keep, which are credentials the session exists to carry.
// The jar offers no enumeration beyond "cookies for this URL", and deletion
// must match the planted (name, domain, path) triple, so each sent name is
// expired in all three shapes an origin can plant: host cookie, host-domain
// cookie, and parent-domain cookie. SSO chains re-run from a clean slate:
// stale session residue makes mlearning answer its forscan entry with an
// immediate 500 instead of starting the redirect chain.
func (s *Session) ClearCookies(rawURL string, keep ...string) {
	if s.jar == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	skip := make(map[string]bool, len(keep))
	for _, name := range keep {
		skip[name] = true
	}
	host := u.Hostname()
	parent := host
	if i := strings.IndexByte(host, '.'); i >= 0 {
		parent = host[i+1:]
	}
	var doomed []*http.Cookie
	for _, c := range s.jar.Cookies(u) {
		if skip[c.Name] {
			continue
		}
		doomed = append(doomed,
			&http.Cookie{Name: c.Name, Path: "/", MaxAge: -1},
			&http.Cookie{Name: c.Name, Path: "/", Domain: "." + host, MaxAge: -1},
			&http.Cookie{Name: c.Name, Path: "/", Domain: "." + parent, MaxAge: -1},
		)
	}
	s.jar.SetCookies(u, doomed)
}
