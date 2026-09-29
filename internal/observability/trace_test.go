package observability

import (
	"context"
	"net/http"
	"testing"
)

func TestTraceContextInjectExtract(t *testing.T) {
	ctx := WithTraceContext(nil, TraceContext{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"})
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	InjectTraceHeaders(ctx, req.Header)
	got, ok := ExtractTraceContext(req.Header)
	if !ok || got.TraceID != "0123456789abcdef0123456789abcdef" || got.SpanID != "0123456789abcdef" {
		t.Fatalf("trace=%+v ok=%v", got, ok)
	}
}

func TestTraceContextRejectsMalformedHeader(t *testing.T) {
	ctx, ok := ExtractTraceContext(http.Header{"Traceparent": []string{"00-bad-00-01"}})
	if ok || ctx.TraceID != "" {
		t.Fatalf("malformed trace accepted: %+v", ctx)
	}
}

func TestNewTraceContextProducesValidIDs(t *testing.T) {
	first, ok := NewTraceContext()
	if !ok {
		t.Fatal("NewTraceContext failed")
	}
	if !validTraceContext(first) {
		t.Fatalf("generated trace is not W3C valid: %+v", first)
	}
	second, _ := NewTraceContext()
	if second.TraceID == first.TraceID || second.SpanID == first.SpanID {
		t.Fatalf("generated ids are not unique: %+v %+v", first, second)
	}
}

func TestEnsureTraceContextPrefersInboundHeader(t *testing.T) {
	header := http.Header{}
	header.Set("Traceparent", "00-0123456789abcdef0123456789abcdef-cafebabe01234567-01")
	if got := EnsureTraceContext(header); got.TraceID != "0123456789abcdef0123456789abcdef" || got.SpanID != "cafebabe01234567" {
		t.Fatalf("inbound trace not reused: %+v", got)
	}
	generated := EnsureTraceContext(http.Header{"Traceparent": []string{"garbage"}})
	if !validTraceContext(generated) {
		t.Fatalf("malformed header did not yield a fresh trace: %+v", generated)
	}
}

func TestStampTraceAddsTraceIDOnlyWhenContextCarriesOne(t *testing.T) {
	if got := StampTrace(context.Background(), nil); got != nil {
		t.Fatalf("untraced context must leave details untouched, got %#v", got)
	}
	ctx := WithTraceContext(context.Background(), TraceContext{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"})
	stamped := StampTrace(ctx, map[string]any{"agentId": "agent-1"})
	if stamped[AuditTraceKey] != "0123456789abcdef0123456789abcdef" || stamped["agentId"] != "agent-1" {
		t.Fatalf("stamped details = %#v", stamped)
	}
	if got := StampTrace(ctx, nil); got[AuditTraceKey] == nil {
		t.Fatalf("nil details should gain the trace id, got %#v", got)
	}
	// A caller that already recorded a trace id, for example a relay that reused
	// the upstream trace, must not be silently rewritten.
	copied := map[string]any{AuditTraceKey: "ffffffffffffffffffffffffffffffff"}
	if got := StampTrace(ctx, copied); got[AuditTraceKey] != "ffffffffffffffffffffffffffffffff" {
		t.Fatalf("existing trace id overwritten: %#v", got)
	}
}
