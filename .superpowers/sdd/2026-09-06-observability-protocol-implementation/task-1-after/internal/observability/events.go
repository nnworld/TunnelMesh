package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
)

const redactedValue = "[redacted]"

// StageEvent is a transport-safe diagnostic event. Sensitive fields are retained only
// long enough to produce a redacted record and are never serialized verbatim.
type StageEvent struct {
	TraceID      string            `json:"trace_id,omitempty"`
	ConnectionID string            `json:"connection_id,omitempty"`
	AgentID      string            `json:"agent_id,omitempty"`
	TokenID      string            `json:"token_id,omitempty"`
	Component    string            `json:"component,omitempty"`
	Stage        string            `json:"stage,omitempty"`
	Result       string            `json:"result,omitempty"`
	ErrorClass   string            `json:"error_class,omitempty"`
	Target       string            `json:"target,omitempty"`
	Payload      string            `json:"payload,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	StartedAt    time.Time         `json:"started_at,omitempty"`
	EndedAt      time.Time         `json:"ended_at,omitempty"`
	Attempt      int               `json:"attempt,omitempty"`
}

func (e StageEvent) MarshalJSON() ([]byte, error) {
	type safeEvent struct {
		TraceID, ConnectionID, AgentID, Component, Stage, Result, ErrorClass string
		TokenID, Target, Payload                                             string
		Metadata                                                             map[string]string
		StartedAt, EndedAt                                                   time.Time
		Attempt                                                              int
	}
	s := safeEvent{TraceID: e.TraceID, ConnectionID: e.ConnectionID, AgentID: e.AgentID, Component: e.Component, Stage: e.Stage, Result: e.Result, ErrorClass: e.ErrorClass, StartedAt: e.StartedAt, EndedAt: e.EndedAt, Attempt: e.Attempt}
	if e.TokenID != "" {
		s.TokenID = redactedValue
	}
	if e.Target != "" {
		s.Target = redactedValue
	}
	if e.Payload != "" {
		s.Payload = redactedValue
	}
	if len(e.Metadata) > 0 {
		s.Metadata = make(map[string]string, len(e.Metadata))
		for key, value := range e.Metadata {
			if sensitiveKey(key) {
				s.Metadata[key] = redactedValue
			} else {
				s.Metadata[key] = value
			}
		}
	}
	return json.Marshal(s)
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, word := range []string{"token", "authorization", "password", "secret", "private_key", "payload", "target", "response", "ssh", "credential"} {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

// NormalizeErrorClass maps arbitrary errors to a small, stable set of classes.
func NormalizeErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "dns"), strings.Contains(message, "no such host"), strings.Contains(message, "lookup"):
		return "dns"
	case strings.Contains(message, "tls"), strings.Contains(message, "certificate"):
		return "tls"
	case strings.Contains(message, "websocket"):
		return "websocket_upgrade"
	case strings.Contains(message, "bearer"), strings.Contains(message, "authentication"), strings.Contains(message, "unauthenticated"), strings.Contains(message, "invalid token"):
		return "authentication"
	case strings.Contains(message, "permission"), strings.Contains(message, "forbidden"), strings.Contains(message, "authorization"):
		return "authorization"
	case strings.Contains(message, "policy"):
		return "policy"
	case strings.Contains(message, "backpressure"), strings.Contains(message, "buffer full"):
		return "backpressure"
	case strings.Contains(message, "reset"), strings.Contains(message, "broken pipe"):
		return "reset"
	case strings.Contains(message, "refused"), strings.Contains(message, "unreachable"), strings.Contains(message, "no route"):
		return "target_unavailable"
	default:
		return "internal"
	}
}
