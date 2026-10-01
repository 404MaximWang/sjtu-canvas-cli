package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxHTMLBody caps one HTML page read. Portal launch and SSO pages are
// small documents; the cap only guards against a misbehaving peer.
const maxHTMLBody = 4 << 20

// run executes req through client with the session's credentials attached
// and logs the exchange. The returned response has an OPEN body.
func (s *Session) run(client *http.Client, req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgent)
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors embed the full URL; scrub it because the query
		// string can carry a credential the redactor never saw.
		err = errors.New(strings.ReplaceAll(err.Error(), req.URL.String(), logURL(req.URL.String())))
		slog.Warn("request", "method", req.Method, "url", logURL(req.URL.String()), "error", err)
		return nil, err
	}
	slog.Info("request", "method", req.Method, "url", logURL(req.URL.String()), "status", resp.StatusCode, "elapsed", time.Since(start))
	return resp, nil
}

// GetHTML fetches one HTML page, following redirects. It returns the body
// and the final URL after redirection, which form actions resolve against.
func (s *Session) GetHTML(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.run(s.client, req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", newHTTPError(resp, http.MethodGet, rawURL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBody))
	if err != nil {
		return nil, "", err
	}
	return body, resp.Request.URL.String(), nil
}

// PostFormHTML submits one form, following redirects, and returns the
// resulting page body and final URL.
func (s *Session) PostFormHTML(ctx context.Context, rawURL string, form url.Values) ([]byte, string, error) {
	resp, err := s.postForm(ctx, s.client, rawURL, form)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", newHTTPError(resp, http.MethodPost, rawURL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBody))
	if err != nil {
		return nil, "", err
	}
	return body, resp.Request.URL.String(), nil
}

// PostFormRedirect submits one form WITHOUT following redirects and returns
// the raw Location header of the 3xx response. Any other status is an error.
// The LTI launch chain carries its token in that redirect, so following it
// would lose exactly the value being extracted.
func (s *Session) PostFormRedirect(ctx context.Context, rawURL string, form url.Values) (string, error) {
	resp, err := s.postForm(ctx, s.noRedirect, rawURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", newHTTPError(resp, http.MethodPost, rawURL)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("POST %s: %d response carries no Location header", logURL(rawURL), resp.StatusCode)
	}
	return location, nil
}

// postForm is the shared body of the two form-posting primitives.
func (s *Session) postForm(ctx context.Context, client *http.Client, rawURL string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.run(client, req)
}
