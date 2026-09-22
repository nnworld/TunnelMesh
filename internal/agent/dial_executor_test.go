package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type executorResult struct {
	result DialResult
	ok     bool
}

type controlledDialFunc struct {
	mu       sync.Mutex
	started  map[int]chan struct{}
	releases map[int]chan struct{}
	results  map[int]func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error)
}

func newControlledDialFunc() *controlledDialFunc {
	return &controlledDialFunc{
		started:  make(map[int]chan struct{}),
		releases: make(map[int]chan struct{}),
		results:  make(map[int]func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error)),
	}
}

func (f *controlledDialFunc) stage(port int, result func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error)) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started[port] == nil {
		f.started[port] = make(chan struct{})
	}
	if f.releases[port] == nil {
		f.releases[port] = make(chan struct{})
	}
	f.results[port] = result
	return f.started[port]
}

func (f *controlledDialFunc) release(port int) {
	f.mu.Lock()
	channel := f.releases[port]
	f.mu.Unlock()
	if channel != nil {
		select {
		case <-channel:
		default:
			close(channel)
		}
	}
}

func (f *controlledDialFunc) dial(ctx context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	started := f.started[payload.TargetPort]
	release := f.releases[payload.TargetPort]
	result := f.results[payload.TargetPort]
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil && result == nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if result != nil {
		return result(ctx, payload)
	}
	return &streamConn{}, nil
}

func executorRequest(id uint32, port int) DialRequest {
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{
		Protocol: "tcp", TargetHost: "service.internal", TargetPort: port,
	})
	return DialRequest{
		Frame:   protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: id, Payload: payload},
		Payload: protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: port},
	}
}

func submitResult(t *testing.T, executor *DialExecutor, id uint32, port int) <-chan executorResult {
	t.Helper()
	result := make(chan executorResult, 1)
	request := executorRequest(id, port)
	request.Result = func(value DialResult) {
		result <- executorResult{result: value, ok: true}
	}
	if err := executor.Submit(context.Background(), request); err != nil {
		result <- executorResult{}
	}
	return result
}

func waitExecutorResult(t *testing.T, result <-chan executorResult) DialResult {
	t.Helper()
	select {
	case value := <-result:
		if !value.ok {
			t.Fatal("dial request was rejected")
		}
		return value.result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dial result")
		return DialResult{}
	}
}

func TestDialExecutorRunsReadyDialWhileOthersBlock(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, nil)
	dial.stage(2, nil)
	dial.stage(3, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		return &streamConn{}, nil
	})
	executor := NewDialExecutorWithDial(DialExecutorConfig{MaxConcurrent: 3, MaxPending: 2, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer executor.Close()

	first := submitResult(t, executor, 1, 1)
	second := submitResult(t, executor, 2, 2)
	<-dial.started[1]
	<-dial.started[2]
	third := submitResult(t, executor, 3, 3)
	result := waitExecutorResult(t, third)
	if result.Code != protocol.OpenResultCodeOK || result.Conn == nil {
		t.Fatalf("ready dial result=%+v, want successful connection", result)
	}
	dial.release(1)
	dial.release(2)
	_ = waitExecutorResult(t, first)
	_ = waitExecutorResult(t, second)
}

func TestDialExecutorRejectsOverflowWithQueueFull(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, nil)
	dial.stage(2, nil)
	executor := NewDialExecutorWithDial(DialExecutorConfig{MaxConcurrent: 1, MaxPending: 1, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer executor.Close()

	first := submitResult(t, executor, 1, 1)
	<-dial.started[1]
	second := submitResult(t, executor, 2, 2)
	overflow := executorRequest(3, 3)
	overflow.Result = func(DialResult) {}
	if err := executor.Submit(context.Background(), overflow); !errors.Is(err, ErrDialQueueFull) {
		t.Fatalf("overflow error=%v, want queue_full", err)
	}
	dial.release(1)
	_ = waitExecutorResult(t, first)
	_ = waitExecutorResult(t, second)
}

func TestDialExecutorCancelCancelsInFlightDial(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, nil)
	executor := NewDialExecutorWithDial(DialExecutorConfig{MaxConcurrent: 1, MaxPending: 1, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer executor.Close()
	result := submitResult(t, executor, 1, 1)
	<-dial.started[1]
	executor.Cancel(1)
	got := waitExecutorResult(t, result)
	if got.Code != protocol.OpenResultCodeTimeout || got.Conn != nil {
		t.Fatalf("cancelled result=%+v, want timeout without connection", got)
	}
}

func TestDialExecutorCloseCancelsAllDials(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, nil)
	dial.stage(2, nil)
	executor := NewDialExecutorWithDial(DialExecutorConfig{MaxConcurrent: 2, MaxPending: 2, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	first := submitResult(t, executor, 1, 1)
	second := submitResult(t, executor, 2, 2)
	<-dial.started[1]
	<-dial.started[2]
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}
	for _, result := range []<-chan executorResult{first, second} {
		if got := waitExecutorResult(t, result); got.Code != protocol.OpenResultCodeTimeout {
			t.Fatalf("closed result=%+v, want timeout", got)
		}
	}
}

func TestDialExecutorMapsConnectionRefused(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})
	executor := NewDialExecutorWithDial(DialExecutorConfig{MaxConcurrent: 1, MaxPending: 1, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer executor.Close()
	result := waitExecutorResult(t, submitResult(t, executor, 1, 1))
	if result.Code != protocol.OpenResultCodeConnectionRefused || result.Stage != protocol.OpenResultStageConnect || result.Err == nil {
		t.Fatalf("result=%+v, want connection_refused at connect", result)
	}
}

func TestAgentStreamCapabilitiesAdvertiseOpenResult(t *testing.T) {
	capabilities := AgentStreamCapabilities(AgentStreamConfig{}, false)
	if len(capabilities) != 1 || capabilities[0] != protocol.CapabilityStreamOpenResult {
		t.Fatalf("capabilities=%v, want stream open result only", capabilities)
	}
}

// The echo capability depends on two independent facts: the operator asked for
// it, and the process actually opened a ping socket. Either one alone must not
// advertise it.
func TestAgentStreamCapabilitiesAdvertiseICMPEchoOnlyWhenConfiguredAndReady(t *testing.T) {
	cases := []struct {
		name    string
		streams AgentStreamConfig
		ready   bool
		want    []string
	}{
		{
			name:    "icmp disabled",
			streams: AgentStreamConfig{},
			ready:   true,
			want:    []string{protocol.CapabilityStreamOpenResult},
		},
		{
			name:    "configured and the socket is open",
			streams: AgentStreamConfig{ICMPEnabled: true},
			ready:   true,
			want:    []string{protocol.CapabilityStreamOpenResult, protocol.CapabilityStreamICMPEcho},
		},
		{
			name:    "configured but the socket did not open",
			streams: AgentStreamConfig{ICMPEnabled: true},
			ready:   false,
			want:    []string{protocol.CapabilityStreamOpenResult},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentStreamCapabilities(tc.streams, tc.ready)
			if len(got) != len(tc.want) {
				t.Fatalf("capabilities = %v, want %v", got, tc.want)
			}
			for index := range got {
				if got[index] != tc.want[index] {
					t.Fatalf("capabilities = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
