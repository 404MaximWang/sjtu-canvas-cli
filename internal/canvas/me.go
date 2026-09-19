// Package canvas holds the minimal Canvas API surface the auth flow needs:
// validating a bearer token against the current-user endpoint. Broader
// course/assignment/file APIs arrive with their own phase.
package canvas

import (
	"context"
	"fmt"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// Self is the slice of GET /api/v1/users/self the CLI displays; Canvas
// returns more fields, and unlisted ones are ignored by design.
type Self struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// VerifyToken calls the current-user endpoint with the token and returns
// the account it authenticates as. A 401 surfaces as a *session.HTTPError,
// which is the "token invalid or expired" signal for callers.
func VerifyToken(ctx context.Context, baseURL, token string) (*Self, error) {
	s := session.NewToken(token)
	var me Self
	if err := s.DoJSON(ctx, "GET", baseURL+"/api/v1/users/self", &me); err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	return &me, nil
}
