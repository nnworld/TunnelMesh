package observability

import (
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
