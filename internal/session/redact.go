package session

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Redaction replaces every registered secret with this marker.
const redacted = "<redacted>"

// redactor holds every secret string currently known to the process
// (Canvas token, JAAuthCookie). Secrets are matched as literal strings;
// SJTU credentials are opaque high-entropy tokens, so exact matching cannot
// produce false positives on ordinary text.
var redactor = struct {
	sync.RWMutex
	secrets []string
}{}

// RegisterSecret adds value to the redaction set. Empty values are ignored:
// matching the empty string would corrupt every message. Call this whenever
// a credential is loaded, stored, or newly issued.
func RegisterSecret(value string) {
	if value == "" {
		return
	}
	redactor.Lock()
	defer redactor.Unlock()
	redactor.secrets = append(redactor.secrets, value)
}

// ForgetSecret drops value from the redaction set, e.g. after logout.
func ForgetSecret(value string) {
	redactor.Lock()
	defer redactor.Unlock()
	kept := redactor.secrets[:0]
	for _, s := range redactor.secrets {
		if s != value {
			kept = append(kept, s)
		}
	}
	redactor.secrets = kept
}

// Redact returns s with every registered secret replaced by "<redacted>".
func Redact(s string) string {
	redactor.RLock()
	defer redactor.RUnlock()
	for _, secret := range redactor.secrets {
		s = strings.ReplaceAll(s, secret, redacted)
	}
	return s
}

// RedactingHandler wraps an slog.Handler and redacts registered secrets from
// every message and attribute value before forwarding. Install it as the
// default logger at startup so no call site can leak credentials by mistake.
type RedactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps inner with secret redaction.
func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

// Enabled reports whether inner handles the level.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle redacts the record's message and string attributes, then forwards.
func (h *RedactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	cleaned := slog.NewRecord(rec.Time, rec.Level, Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(Redact(a.Value.String()))
		}
		cleaned.AddAttrs(a)
		return true
	})
	return h.inner.Handle(ctx, cleaned)
}

// WithAttrs forwards to the wrapped handler.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup forwards to the wrapped handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}
