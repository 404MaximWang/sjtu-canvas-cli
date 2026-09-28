package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxErrorBody caps how much of an error response is echoed back; Canvas
// error pages can be large HTML documents, and only the beginning carries
// the useful message.
const maxErrorBody = 512

// HTTPError describes a non-2xx response. The body excerpt is redacted
// before storage, so an HTTPError is always safe to log or print.
type HTTPError struct {
	Method string
	URL    string
	Status string
	Body   string
}

// Error formats the failure for humans.
func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.URL, e.Status, e.Body)
}

// do issues one request with the session's credentials attached and logs the
// exchange. The returned response has an OPEN body; use DoJSON, DoJSONHeader,
// or DoStream instead of calling this directly.
func (s *Session) do(ctx context.Context, method, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		// Transport errors embed the full URL; scrub it because the query
		// string can carry a file-download verifier the redactor never saw.
		err = errors.New(strings.ReplaceAll(err.Error(), rawURL, logURL(rawURL)))
		slog.Warn("request", "method", method, "url", logURL(rawURL), "error", err)
		return nil, err
	}
	slog.Info("request", "method", method, "url", logURL(rawURL), "status", resp.StatusCode, "elapsed", time.Since(start))
	return resp, nil
}

// logURL strips the query string from rawURL for log lines: Canvas file URLs
// carry a verifier token in the query that grants download access, and the
// redactor only knows about long-lived credentials.
func logURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparseable URL)"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// DoJSON performs a JSON request and fully manages the response body: the
// caller receives the value decoded into out, or an error, and never an open
// body. out may be nil for requests whose payload is irrelevant.
func (s *Session) DoJSON(ctx context.Context, method, rawURL string, out any) error {
	_, err := s.doJSON(ctx, method, rawURL, out)
	return err
}

// DoJSONHeader behaves like DoJSON but also returns the response headers,
// which Canvas list pagination needs for the Link header.
func (s *Session) DoJSONHeader(ctx context.Context, method, rawURL string, out any) (http.Header, error) {
	return s.doJSON(ctx, method, rawURL, out)
}

// doJSON is the shared body of DoJSON and DoJSONHeader. defer binds to
// function return, not to a scope; doJSON performs a single request per
// call, so closing here is exact.
func (s *Session) doJSON(ctx context.Context, method, rawURL string, out any) (http.Header, error) {
	resp, err := s.do(ctx, method, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newHTTPError(resp, method, rawURL)
	}
	if out == nil {
		// Drain so the underlying connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Header, nil
	}
	return resp.Header, json.NewDecoder(resp.Body).Decode(out)
}

// DoStream performs a request whose body the CALLER must close. It is
// reserved for large or unbounded payloads (video download, live streams);
// everything else belongs in DoJSON.
func (s *Session) DoStream(ctx context.Context, method, rawURL string) (*http.Response, error) {
	resp, err := s.do(ctx, method, rawURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, newHTTPError(resp, method, rawURL)
	}
	return resp, nil
}

// newHTTPError builds an HTTPError with a redacted, length-capped excerpt of
// the response body.
func newHTTPError(resp *http.Response, method, rawURL string) *HTTPError {
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &HTTPError{
		Method: method,
		URL:    rawURL,
		Status: resp.Status,
		Body:   Redact(string(excerpt)),
	}
}
