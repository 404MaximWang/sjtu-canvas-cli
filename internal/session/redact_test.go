package session

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// Throwaway smoke test: a registered secret must never reach log output.
func TestRedactingHandlerHidesSecrets(t *testing.T) {
	RegisterSecret("super-secret-token-abc123")
	defer ForgetSecret("super-secret-token-abc123")

	var buf bytes.Buffer
	slog.SetDefault(slog.New(NewRedactingHandler(slog.NewTextHandler(&buf, nil))))
	slog.Info("token is super-secret-token-abc123", "cookie", "JAAuthCookie=super-secret-token-abc123")

	if strings.Contains(buf.String(), "super-secret-token-abc123") {
		t.Fatalf("secret leaked into log output: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "<redacted>") {
		t.Fatalf("expected redaction marker in: %s", buf.String())
	}
}
