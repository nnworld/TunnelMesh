package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

type TraceContext struct {
	TraceID string
	SpanID  string
}
type traceContextKey struct{}

func WithTraceContext(ctx context.Context, trace TraceContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validTraceContext(trace) {
		return ctx
	}
	return context.WithValue(ctx, traceContextKey{}, trace)
}
func TraceContextFromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	trace, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return trace, ok && validTraceContext(trace)
}
func InjectTraceHeaders(ctx context.Context, header http.Header) {
	if header == nil {
		return
	}
	trace, ok := TraceContextFromContext(ctx)
	if ok {
		header.Set("Traceparent", "00-"+trace.TraceID+"-"+trace.SpanID+"-01")
	}
}
func ExtractTraceContext(header http.Header) (TraceContext, bool) {
	value := strings.TrimSpace(header.Get("Traceparent"))
	parts := strings.Split(value, "-")
	if len(parts) != 4 || parts[0] != "00" || parts[3] == "00" {
		return TraceContext{}, false
	}
	trace := TraceContext{TraceID: parts[1], SpanID: parts[2]}
	if !validTraceContext(trace) {
		return TraceContext{}, false
	}
	return trace, true
}

// TraceIDFromContext reports the trace id of the active request, or the empty
// string when the context carries no valid trace.
func TraceIDFromContext(ctx context.Context) string {
	trace, ok := TraceContextFromContext(ctx)
	if !ok {
		return ""
	}
	return trace.TraceID
}

// AuditTraceKey is the audit details field that carries the W3C trace id of the
// request that produced the event. Keeping the key in one place is what lets the
// console and the OpenAPI document agree on its name.
const AuditTraceKey = "traceId"

// StampTrace adds the active trace id to an audit details map so an operator can
// jump from an audit row to the structured logs of the same request. It is a
// deliberate no-op when the context carries no trace, which keeps background
// jobs and unit tests writing byte-identical details; it never overwrites a
// trace id the caller already recorded.
func StampTrace(ctx context.Context, details map[string]any) map[string]any {
	traceID := TraceIDFromContext(ctx)
	if traceID == "" {
		return details
	}
	if details == nil {
		details = map[string]any{}
	}
	if _, ok := details[AuditTraceKey]; !ok {
		details[AuditTraceKey] = traceID
	}
	return details
}

// NewTraceContext mints a random, W3C-valid trace and span id so a management
// request that arrives without Traceparent is still correlatable end to end.
func NewTraceContext() (TraceContext, bool) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return TraceContext{}, false
	}
	return TraceContext{TraceID: hex.EncodeToString(raw[:16]), SpanID: hex.EncodeToString(raw[16:])}, true
}

// EnsureTraceContext returns the trace carried by an inbound header, generating
// a fresh one when it is missing or malformed.
func EnsureTraceContext(header http.Header) TraceContext {
	if trace, ok := ExtractTraceContext(header); ok {
		return trace
	}
	trace, _ := NewTraceContext()
	return trace
}

func validTraceContext(trace TraceContext) bool {
	if len(trace.TraceID) != 32 || len(trace.SpanID) != 16 {
		return false
	}
	if _, err := hex.DecodeString(trace.TraceID); err != nil {
		return false
	}
	if _, err := hex.DecodeString(trace.SpanID); err != nil {
		return false
	}
	return strings.Trim(trace.TraceID, "0") != "" && strings.Trim(trace.SpanID, "0") != ""
}
