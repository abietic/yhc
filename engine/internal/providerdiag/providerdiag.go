// Package providerdiag carries bounded, content-free provider stream evidence.
package providerdiag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Snapshot is deliberately restricted to structural transport and stream facts.
// Empty fields mean the adapter did not observe that fact.
type Snapshot struct {
	HTTPStatus             int    `json:"http_status,omitempty"`
	RequestIDHash          string `json:"request_id_hash,omitempty"`
	ResponseIDHash         string `json:"response_id_hash,omitempty"`
	TransportPhase         string `json:"transport_phase,omitempty"`
	TerminationKind        string `json:"termination_kind,omitempty"`
	StreamEventCount       uint64 `json:"stream_event_count"`
	BodyBytes              uint64 `json:"body_bytes"`
	LastValidatedSequence  *int64 `json:"last_validated_sequence,omitempty"`
	ValidatedTerminalEvent bool   `json:"validated_terminal_event"`
	AuthoritativeUsage     bool   `json:"authoritative_usage"`
}

// Observer is per admitted call. It is safe for the transport/parser and the
// consumer settlement path to use concurrently.
type Observer struct {
	mu   sync.Mutex
	s    Snapshot
	sink func(Snapshot)
}

// New installs a synchronous sink. The sink must not reenter this observer;
// execution only acquires the usage-owner mutex and never the reverse order.
func New(sink func(Snapshot)) *Observer { return &Observer{sink: sink} }

func HashIdentifier(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (o *Observer) Headers(status int, requestIDHash string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if status >= 100 && status <= 599 {
		o.s.HTTPStatus = status
	}
	if o.s.RequestIDHash == "" && validHash(requestIDHash) {
		o.s.RequestIDHash = requestIDHash
	}
	if phaseRank("headers") > phaseRank(o.s.TransportPhase) {
		o.s.TransportPhase = "headers"
	}
	o.observeLocked()
}

func (o *Observer) Transport(phase string) {
	if o == nil {
		return
	}
	switch phase {
	case "dns", "connect", "tls", "upload", "request_sent", "headers", "body":
	default:
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	// phases are ordered, so a later phase cannot be overwritten by a stale hook.
	if phaseRank(phase) > phaseRank(o.s.TransportPhase) {
		o.s.TransportPhase = phase
		o.observeLocked()
	}
}

func phaseRank(v string) int {
	switch v {
	case "dns":
		return 1
	case "connect":
		return 2
	case "tls":
		return 3
	case "upload":
		return 4
	case "request_sent":
		return 5
	case "headers":
		return 6
	case "body":
		return 7
	}
	return 0
}

func (o *Observer) Body(n int) {
	if o != nil && n > 0 {
		o.mu.Lock()
		o.s.BodyBytes += uint64(n)
		o.s.TransportPhase = "body"
		o.observeLocked()
		o.mu.Unlock()
	}
}

func (o *Observer) Event(sequence int64) {
	if o != nil {
		o.mu.Lock()
		o.s.StreamEventCount++
		n := sequence
		o.s.LastValidatedSequence = &n
		o.observeLocked()
		o.mu.Unlock()
	}
}

func (o *Observer) Terminal(responseIDHash string, usage bool) {
	if o != nil {
		o.mu.Lock()
		o.s.ValidatedTerminalEvent = true
		o.s.AuthoritativeUsage = usage
		if o.s.ResponseIDHash == "" && validHash(responseIDHash) {
			o.s.ResponseIDHash = responseIDHash
		}
		o.observeLocked()
		o.mu.Unlock()
	}
}

func (o *Observer) Terminate(kind string) {
	if o == nil {
		return
	}
	switch kind {
	case "completed", "incomplete", "api_error", "protocol_error", "transport_error", "canceled", "deadline_exceeded", "eof_missing_terminal":
	default:
		return
	}
	o.mu.Lock()
	if o.s.TerminationKind == "" {
		o.s.TerminationKind = kind
	}
	o.observeLocked()
	o.mu.Unlock()
}

func (o *Observer) ResponseID(responseIDHash string) {
	if o == nil || !validHash(responseIDHash) {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.s.ResponseIDHash == "" {
		o.s.ResponseIDHash = responseIDHash
	}
	o.observeLocked()
}

func (o *Observer) observeLocked() {
	if o.sink != nil {
		o.sink(copySnapshot(o.s))
	}
}

func (o *Observer) Snapshot() Snapshot {
	if o == nil {
		return Snapshot{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return copySnapshot(o.s)
}

func copySnapshot(s Snapshot) Snapshot {
	if s.LastValidatedSequence != nil {
		v := *s.LastValidatedSequence
		s.LastValidatedSequence = &v
	}
	return s
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type contextKey struct{}

func WithObserver(ctx context.Context, o *Observer) context.Context {
	if ctx == nil {
		return nil
	}
	// A nil observer masks an enclosing call's observer in helper contexts.
	return context.WithValue(ctx, contextKey{}, o)
}

func FromContext(ctx context.Context) *Observer {
	if ctx == nil {
		return nil
	}
	o, _ := ctx.Value(contextKey{}).(*Observer)
	return o
}
