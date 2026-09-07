package observability

import (
	"context"
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
