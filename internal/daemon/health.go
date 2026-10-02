package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Health states reported by /healthz.
const (
	stateOK      = "ok"
	stateFail    = "fail"
	stateUnknown = "unknown"
)

// DomainState is one domain's health as healthz reports it.
type DomainState struct {
	State  string `json:"state"`            // ok | fail | unknown
	Detail string `json:"detail,omitempty"` // failure remediation or unknown reason
	At     string `json:"at"`               // RFC3339 second of the last state change
}

// health tracks per-domain states and fires one notification per
// transition into fail and back to ok — including a fail on the very
// first round, and never a repeat while the domain keeps failing.
type health struct {
	mu     sync.Mutex
	states map[string]DomainState
	notify func(ctx context.Context, event, domain string, st DomainState)
}

func newHealth(notify func(ctx context.Context, event, domain string, st DomainState)) *health {
	h := &health{
		states: make(map[string]DomainState, len(Domains)),
		notify: notify,
	}
	at := time.Now().Format(time.RFC3339)
	for _, domain := range Domains {
		h.states[domain] = DomainState{State: stateUnknown, Detail: "not probed yet", At: at}
	}
	return h
}

// set records one outcome for domain and notifies on transitions.
func (h *health) set(ctx context.Context, domain, state, detail string) {
	h.mu.Lock()
	prev := h.states[domain]
	cur := DomainState{State: state, Detail: detail, At: prev.At}
	if prev.State != state {
		cur.At = time.Now().Format(time.RFC3339)
	}
	h.states[domain] = cur
	h.mu.Unlock()

	switch {
	case state == stateFail && prev.State != stateFail:
		h.notify(ctx, "fail", domain, cur)
	case state == stateOK && prev.State == stateFail:
		h.notify(ctx, "recover", domain, cur)
	}
}

// snapshot returns a copy of every domain's state.
func (h *health) snapshot() map[string]DomainState {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]DomainState, len(h.states))
	for domain, st := range h.states {
		out[domain] = st
	}
	return out
}

// noteRequestOutcome refreshes health from a real request's result: an
// auth failure that survived the reload-and-retry marks the credential
// domain failed, and a success after a reload marks it recovered. Failures
// that are not credential-related never reach here.
func (d *Daemon) noteRequestOutcome(ctx context.Context, credDomain string, ok bool) {
	domain := credDomain // canvas maps to itself; the rest share jaccount
	if credDomain != DomainCanvas {
		domain = DomainJAccount
	}
	if ok {
		d.health.set(ctx, domain, stateOK, "")
	} else {
		d.health.set(ctx, domain, stateFail, remediation(domain))
	}
}

// handleHealthz reports every domain's state; the HTTP status is always
// 200, the states carry the information.
func (d *Daemon) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"domains": d.health.snapshot()})
}
