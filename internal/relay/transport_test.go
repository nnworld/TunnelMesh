package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestRelayMetadataPreservesUpstreamDomainAndTLSOptions(t *testing.T) {
	req := StreamRequest{
		NodeID: "node-a", AgentID: "agent-a", StreamID: 7, Protocol: "http", StrictOpen: true,
		TargetConnectionID: "conn-a", TargetConnectionEpoch: 9, InitialWindow: 4096,
		TargetHost: "10.0.0.1", TargetPort: 443, TargetScheme: "https",
		HostHeader: "service.internal.example.com", TLSServerName: "service.internal.example.com",
	}

	meta, err := structpb.NewStruct(relayStreamMetadata(req))
	if err != nil {
		t.Fatal(err)
	}
	got := streamRequestFromRelayMetadata(meta.GetFields())

	if !reflect.DeepEqual(got, req) {
		t.Fatalf("metadata round trip = %#v, want %#v", got, req)
	}
}

type relayControlTestConn struct {
	controls      chan protocol.Frame
	agentControls chan protocol.Frame
	agentReady    chan struct{}
	writes        chan []byte
	closed        chan struct{}
	closeOnce     sync.Once
}

func newRelayControlTestConn() *relayControlTestConn {
	return &relayControlTestConn{
		controls: make(chan protocol.Frame, 4), agentControls: make(chan protocol.Frame, 4),
		agentReady: make(chan struct{}, 1), writes: make(chan []byte, 4), closed: make(chan struct{}),
	}
}

func (c *relayControlTestConn) Read([]byte) (int, error) {
	select {
	case <-c.agentReady:
		return 0, nil
	case <-c.closed:
		return 0, io.EOF
	}
}
func (c *relayControlTestConn) Write(payload []byte) (int, error) {
	select {
	case c.writes <- append([]byte(nil), payload...):
		return len(payload), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}
func (c *relayControlTestConn) WriteControl(frame protocol.Frame) error {
	select {
	case c.controls <- frame:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}
func (c *relayControlTestConn) ReadControl() (protocol.Frame, bool) {
	select {
	case frame := <-c.agentControls:
		select {
		case <-c.agentReady:
		default:
		}
		return frame, true
	default:
		return protocol.Frame{}, false
	}
}
func (c *relayControlTestConn) CloseWrite() error { return nil }
func (c *relayControlTestConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func TestGRPCNodeTransportPropagatesWindowControlFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server := grpc.NewServer()
	defer server.Stop()
	remoteConn := newRelayControlTestConn()
	RegisterRelayServer(server, NewRelayServer(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return remoteConn, nil
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener)
	conn, err := grpc.DialContext(ctx, listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	client := &GRPCNodeTransport{conn: conn}
	defer client.Close()
	stream, err := client.OpenStream(ctx, StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22, InitialWindow: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	controlWriter, ok := stream.(interface{ WriteControl(protocol.Frame) error })
	if !ok {
		t.Fatal("GRPC stream does not support control writes")
	}
	if err := controlWriter.WriteControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 1, Window: 8}); err != nil {
		t.Fatal(err)
	}
	select {
	case control := <-remoteConn.controls:
		if control.Type != protocol.FrameWindowUpdate || control.Window != 8 {
			t.Fatalf("remote WINDOW_UPDATE=%+v, want window 8", control)
		}
	case <-time.After(time.Second):
		t.Fatal("WINDOW_UPDATE was not forwarded to remote relay")
	}

	remoteConn.agentControls <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 1, Window: 16}
	remoteConn.agentReady <- struct{}{}
	go func() {
		_, _ = stream.Read(make([]byte, 1))
	}()
	controlReader, ok := stream.(interface{ ReadControl() (protocol.Frame, bool) })
	if !ok {
		t.Fatal("GRPC stream does not support control reads")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if control, available := controlReader.ReadControl(); available {
			if control.Type != protocol.FrameWindowUpdate || control.Window != 16 {
				t.Fatalf("client WINDOW_UPDATE=%+v, want window 16", control)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("remote WINDOW_UPDATE was not forwarded to client")
}

type immediateConn struct{ payload []byte }

func (c immediateConn) Read(buffer []byte) (int, error) {
	if len(c.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, c.payload)
	c.payload = c.payload[n:]
	return n, nil
}

func (immediateConn) Write([]byte) (int, error) { return 0, nil }
func (immediateConn) Close() error              { return nil }

func TestGRPCNodeTransportOpenStreamResultRejectsLegacyRemote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := grpc.NewServer()
	defer server.Stop()
	RegisterRelayServer(server, NewRelayServer(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return immediateConn{payload: []byte("legacy-data")}, nil
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener)
	conn, err := grpc.DialContext(ctx, listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	client := &GRPCNodeTransport{conn: conn}
	defer client.Close()
	stream, result, openErr := client.OpenStreamResult(ctx, StreamRequest{StrictOpen: true, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22})
	if stream != nil || openErr != nil || result.Payload.Accepted || result.Payload.Code != protocol.OpenResultCodeUnsupportedCapability {
		t.Fatalf("stream=%v result=%+v err=%v, want unsupported capability", stream, result.Payload, openErr)
	}
}

func TestGRPCNodeTransportOpenStreamResultDoesNotLeakHandlerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := grpc.NewServer()
	defer server.Stop()
	RegisterRelayServer(server, NewRelayServerWithOpenResult(func(context.Context, RelayOpenMetadata) (io.ReadWriteCloser, RelayOpenResult, error) {
		return nil, RelayOpenResult{}, errors.New("token server-secret production-dsn")
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener)
	conn, err := grpc.DialContext(ctx, listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	client := &GRPCNodeTransport{conn: conn}
	defer client.Close()
	stream, result, openErr := client.OpenStreamResult(ctx, StreamRequest{StrictOpen: true, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22})
	if stream != nil || openErr != nil || result.Payload.Accepted || result.Payload.Code != protocol.OpenResultCodeInternalError {
		t.Fatalf("stream=%v result=%+v err=%v, want internal failure", stream, result.Payload, openErr)
	}
}

func TestGRPCNodeTransportOpenStreamResultPropagatesRemoteResult(t *testing.T) {
	tests := []struct {
		name     string
		accepted bool
		code     string
	}{
		{name: "success", accepted: true},
		{name: "refused", accepted: false, code: "connection_refused"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			server := grpc.NewServer()
			defer server.Stop()
			RegisterRelayServer(server, NewRelayServerWithOpenResult(func(context.Context, RelayOpenMetadata) (io.ReadWriteCloser, RelayOpenResult, error) {
				if test.accepted {
					return newEchoConn(), RelayOpenResult{Payload: protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}}, nil
				}
				return nil, RelayOpenResult{Payload: protocol.OpenResultPayload{Accepted: false, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeConnectionRefused}}, nil
			}))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go server.Serve(listener)

			conn, err := grpc.DialContext(ctx, listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
			if err != nil {
				t.Fatal(err)
			}
			client := &GRPCNodeTransport{conn: conn}
			defer client.Close()
			stream, result, openErr := client.OpenStreamResult(ctx, StreamRequest{StrictOpen: true, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22})
			if openErr != nil {
				t.Fatal(openErr)
			}
			if result.Payload.Accepted != test.accepted || result.Payload.Stage != protocol.OpenResultStageConnect {
				t.Fatalf("result=%+v, want accepted=%v", result.Payload, test.accepted)
			}
			if test.accepted {
				if stream == nil {
					t.Fatal("successful result did not expose stream")
				}
				if _, err := stream.Write([]byte("hello")); err != nil {
					t.Fatal(err)
				}
				buffer := make([]byte, 5)
				if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "hello" {
					t.Fatalf("echo=%q err=%v", buffer, err)
				}
				_ = stream.Close()
				return
			}
			if stream != nil || result.Payload.Code != protocol.OpenResultCodeConnectionRefused {
				t.Fatalf("stream=%v result=%+v, want refused failure without stream", stream, result.Payload)
			}
		})
	}
}
