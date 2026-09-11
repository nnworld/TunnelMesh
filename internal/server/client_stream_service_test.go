package server

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type blockingAuthorizeTransport struct {
	mu         sync.Mutex
	calls      int
	blockAgent string
	release    chan struct{}
	openErr    error
	started    chan struct{}
}

func (t *blockingAuthorizeTransport) Authorize(ctx context.Context, _ ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
	t.mu.Lock()
	t.calls++
	block := request.AgentID == t.blockAgent
	t.mu.Unlock()
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	if block {
		select {
		case <-t.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (t *blockingAuthorizeTransport) OpenStream(_ context.Context, _ relay.StreamRequest) (io.ReadWriteCloser, error) {
	if t.openErr != nil {
		return nil, t.openErr
	}
	server, _ := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, server) }()
	return server, nil
}

func (*blockingAuthorizeTransport) Close() error { return nil }

func (t *blockingAuthorizeTransport) OpenStreamResult(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, relay.RelayOpenResult, error) {
	conn, err := t.OpenStream(ctx, request)
	if err != nil || conn == nil {
		return nil, relay.RelayOpenResult{Payload: protocol.OpenResultPayload{Accepted: false, Stage: protocol.OpenResultStageRelay, Code: protocol.OpenResultCodeInternalError}}, err
	}
	return conn, relay.RelayOpenResult{Payload: protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}}, nil
}

type legacyOpenTransport struct{ inner *blockingAuthorizeTransport }

func (t legacyOpenTransport) OpenStream(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	return t.inner.OpenStream(ctx, request)
}

func (legacyOpenTransport) Close() error { return nil }

type contextBoundTransport struct{ inner *blockingAuthorizeTransport }

func (t contextBoundTransport) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
	return t.inner.Authorize(ctx, principal, request)
}

func (t contextBoundTransport) OpenStreamResult(ctx context.Context, _ relay.StreamRequest) (io.ReadWriteCloser, relay.RelayOpenResult, error) {
	return &contextBoundStream{ctx: ctx}, relay.RelayOpenResult{
		Payload: protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK},
	}, nil
}

func (contextBoundTransport) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	return nil, relay.ErrNodeDisconnected
}

func (contextBoundTransport) Close() error { return nil }

type contextBoundStream struct{ ctx context.Context }

func (s contextBoundStream) Read([]byte) (int, error)  { return 0, s.ctx.Err() }
func (s contextBoundStream) Write([]byte) (int, error) { return 0, s.ctx.Err() }
func (s contextBoundStream) Close() error              { return nil }

func strictPrincipal() ClientSessionPrincipal {
	return ClientSessionPrincipal{ConnectionID: "connection", StrictOpen: true, Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}
}

func openRequest(agentID string) protocol.StreamOpenPayload {
	return protocol.StreamOpenPayload{AgentID: agentID, Protocol: "tcp", TargetHost: "service.internal", TargetPort: 80}
}

func waitOpenResult(t *testing.T, future OpenFuture) protocol.OpenResultPayload {
	t.Helper()
	result, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Open().Wait() error = %v", err)
	}
	return result
}

func TestClientStreamServiceReadyOpenCompletesWhileAnotherAuthorizes(t *testing.T) {
	inner := &blockingAuthorizeTransport{blockAgent: "blocked", release: make(chan struct{})}
	inner.started = make(chan struct{}, 1)
	service := NewClientStreamService(inner, inner, ClientStreamServiceConfig{MaxConcurrentOpens: 2, MaxPendingOpens: 2, OpenTimeout: time.Second}, nil)
	defer service.Close()
	blocked := service.Open(context.Background(), strictPrincipal(), 1, openRequest("blocked"))
	<-inner.started
	ready := service.Open(context.Background(), strictPrincipal(), 2, openRequest("ready"))
	result := waitOpenResult(t, ready)
	if !result.Accepted || result.Code != protocol.OpenResultCodeOK {
		t.Fatalf("ready result=%+v, want success", result)
	}
	if futureStream := ready.Stream(); futureStream == nil {
		t.Fatal("successful future did not expose relay stream")
	}
	close(inner.release)
	_ = waitOpenResult(t, blocked)
}

func TestClientStreamServiceRejectsOverflowWithQueueFull(t *testing.T) {
	inner := &blockingAuthorizeTransport{blockAgent: "blocked", release: make(chan struct{})}
	inner.started = make(chan struct{}, 1)
	service := NewClientStreamService(inner, inner, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
	defer service.Close()
	_ = service.Open(context.Background(), strictPrincipal(), 1, openRequest("blocked"))
	<-inner.started
	_ = service.Open(context.Background(), strictPrincipal(), 2, openRequest("blocked"))
	overflow := service.Open(context.Background(), strictPrincipal(), 3, openRequest("blocked"))
	result := waitOpenResult(t, overflow)
	if result.Accepted || result.Code != protocol.OpenResultCodeQueueFull || !result.Retryable {
		t.Fatalf("overflow result=%+v, want retryable queue_full", result)
	}
	close(inner.release)
}

func TestClientStreamServiceMapsStableFailures(t *testing.T) {
	tests := []struct {
		name    string
		authErr error
		openErr error
		want    protocol.OpenResultCode
	}{
		{name: "authorization", authErr: auth.ErrForbidden, want: protocol.OpenResultCodeForbidden},
		{name: "agent offline", openErr: relay.ErrNodeDisconnected, want: protocol.OpenResultCodeAgentOffline},
		{name: "internal", openErr: errors.New("private failure"), want: protocol.OpenResultCodeInternalError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inner := &blockingAuthorizeTransport{openErr: test.openErr}
			if test.authErr != nil {
				inner = &blockingAuthorizeTransport{}
				service := NewClientStreamService(streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return test.authErr }), inner, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
				defer service.Close()
				result := waitOpenResult(t, service.Open(context.Background(), strictPrincipal(), 1, openRequest("agent")))
				if result.Accepted || result.Code != test.want {
					t.Fatalf("result=%+v, want code %s", result, test.want)
				}
				return
			}
			service := NewClientStreamService(inner, inner, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
			defer service.Close()
			result := waitOpenResult(t, service.Open(context.Background(), strictPrincipal(), 1, openRequest("agent")))
			if result.Accepted || result.Code != test.want {
				t.Fatalf("result=%+v, want code %s", result, test.want)
			}
		})
	}
}

func TestClientStreamServiceStrictOpenDoesNotRejectRemoteCapableTransport(t *testing.T) {
	inner := &blockingAuthorizeTransport{}
	service := NewClientStreamService(inner, inner, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
	defer service.Close()
	result := waitOpenResult(t, service.Open(context.Background(), strictPrincipal(), 1, openRequest("agent")))
	if !result.Accepted || result.Code != protocol.OpenResultCodeOK {
		t.Fatalf("result=%+v, want success from result-capable selected transport", result)
	}
}

func TestClientStreamServiceSuccessDoesNotCancelRelayStreamContext(t *testing.T) {
	inner := &blockingAuthorizeTransport{}
	service := NewClientStreamService(inner, contextBoundTransport{inner: inner}, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
	defer service.Close()
	future := service.Open(context.Background(), strictPrincipal(), 1, openRequest("agent"))
	result := waitOpenResult(t, future)
	if !result.Accepted || future.Stream() == nil {
		t.Fatalf("result=%+v stream=%v, want successful stream", result, future.Stream())
	}
	// gRPC relay streams inherit the context passed to OpenStreamResult. The
	// open worker must not cancel it after the stream is successfully handed
	// back to the client session.
	if _, err := future.Stream().Write([]byte("still-open")); err != nil {
		t.Fatalf("successful relay stream write after open: %v", err)
	}
}

func TestClientStreamServiceStrictOpenRequiresResultCapableTransport(t *testing.T) {
	inner := &blockingAuthorizeTransport{}
	service := NewClientStreamService(inner, legacyOpenTransport{inner: inner}, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
	defer service.Close()

	future := service.Open(context.Background(), strictPrincipal(), 1, openRequest("agent"))
	result := waitOpenResult(t, future)
	if result.Accepted || result.Code != protocol.OpenResultCodeUnsupportedCapability {
		t.Fatalf("result=%+v, want unsupported capability", result)
	}
	if stream := future.Stream(); stream != nil {
		t.Fatalf("strict open exposed stream %v, want nil", stream)
	}
}

func TestClientStreamServiceOpenTimeout(t *testing.T) {
	inner := &blockingAuthorizeTransport{blockAgent: "blocked", release: make(chan struct{})}
	service := NewClientStreamService(inner, inner, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: 20 * time.Millisecond}, nil)
	defer service.Close()
	defer close(inner.release)
	result := waitOpenResult(t, service.Open(context.Background(), strictPrincipal(), 1, openRequest("blocked")))
	if result.Accepted || result.Code != protocol.OpenResultCodeTimeout || !result.Retryable {
		t.Fatalf("result=%+v, want retryable timeout", result)
	}
}
