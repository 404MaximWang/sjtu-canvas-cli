// Package canvas holds the Canvas API surface: bearer-token validation
// against the current-user endpoint, plus the course/assignment/file reads
// backing the vfs.
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

// Me returns the account the client's token authenticates as. A 401 surfaces
// as a *session.HTTPError, which is the "token invalid or expired" signal
// for callers.
func (c *Client) Me(ctx context.Context) (*Self, error) {
	var me Self
	if err := c.sess.DoJSON(ctx, "GET", c.base+"/api/v1/users/self", &me); err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	return &me, nil
}

// Verify checks that the client's token still authenticates.
func (c *Client) Verify(ctx context.Context) error {
	_, err := c.Me(ctx)
	return err
}

// VerifyToken authenticates with a bare token, for the login flow where no
// client exists yet.
func VerifyToken(ctx context.Context, baseURL, token string) (*Self, error) {
	return New(session.NewToken(token), baseURL).Me(ctx)
}
