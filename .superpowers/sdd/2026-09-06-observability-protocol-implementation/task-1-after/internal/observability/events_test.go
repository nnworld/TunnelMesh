package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestStageEventRedactsSecretsTargetsAndPayloads(t *testing.T) {
	event := StageEvent{
		TraceID:      "trace-1",
		ConnectionID: "conn-1",
		TokenID:      "token-secret",
		Component:    "server",
		Stage:        "websocket_upgrade",
		Result:       "failure",
		ErrorClass:   NormalizeErrorClass(errors.New("dial tcp 10.0.0.8:443: connection refused")),
		Target:       "10.0.0.8:443",
		Payload:      "Authorization: Bearer super-secret",
		Metadata: map[string]string{
			"authorization": "Bearer super-secret",
			"region":        "cn-east-1",
		},
		StartedAt: time.Unix(10, 0),
		EndedAt:   time.Unix(11, 0),
	}

	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(b)
	for _, secret := range []string{"token-secret", "10.0.0.8:443", "Bearer super-secret", "super-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("event leaked sensitive value %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "[redacted]") || !strings.Contains(text, "cn-east-1") {
		t.Fatalf("event did not preserve safe fields and redaction marker: %s", text)
	}
}

func TestNormalizeErrorClassIsBounded(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "internal.example"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "dns", err: dnsErr, want: "dns"},
		{name: "timeout", err: context.DeadlineExceeded, want: "timeout"},
		{name: "authentication", err: errors.New("invalid bearer token"), want: "authentication"},
		{name: "authorization", err: errors.New("permission denied"), want: "authorization"},
		{name: "reset", err: errors.New("connection reset by peer"), want: "reset"},
		{name: "target", err: errors.New("connection refused"), want: "target_unavailable"},
		{name: "internal", err: errors.New("a unique user payload 123"), want: "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeErrorClass(tc.err); got != tc.want {
				t.Fatalf("NormalizeErrorClass() = %q, want %q", got, tc.want)
			}
		})
	}
}
