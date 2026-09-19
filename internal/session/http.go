package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// do issues one request with the session's credentials attached. The
// returned response has an OPEN body; use DoJSON or DoStream instead of
// calling this directly.
func (s *Session) do(ctx context.Context, method, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return s.client.Do(req)
}

// DoJSON performs a JSON request and fully manages the response body: the
// caller receives the value decoded into out, or an error, and never an open
// body. out may be nil for requests whose payload is irrelevant.
func (s *Session) DoJSON(ctx context.Context, method, rawURL string, out any) error {
	resp, err := s.do(ctx, method, rawURL)
	if err != nil {
		return err
	}
	// defer binds to function return, not to a scope; doJSON performs a
	// single request per call, so closing here is exact.
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newHTTPError(resp, method, rawURL)
	}
	if out == nil {
		// Drain so the underlying connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
