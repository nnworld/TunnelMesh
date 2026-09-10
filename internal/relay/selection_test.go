package relay

import (
	"context"
	"io"
	"testing"
)

type recordingTransport struct {
	request StreamRequest
}

func (t *recordingTransport) OpenStream(_ context.Context, request StreamRequest) (io.ReadWriteCloser, error) {
	t.request = request
	return nopStream{}, nil
}
func (t *recordingTransport) Close() error { return nil }

type nopStream struct{}

func (nopStream) Read([]byte) (int, error)  { return 0, io.EOF }
func (nopStream) Write([]byte) (int, error) { return 0, nil }
func (nopStream) Close() error              { return nil }

type fixedSelector struct {
	target AgentConnectionTarget
}

func (s fixedSelector) Select(context.Context, string, string) (AgentConnectionTarget, error) {
	return s.target, nil
}

func TestSelectedTransportPrefersLocalAndPinsConnection(t *testing.T) {
	local := &recordingTransport{}
	transport := NewSelectedTransport(fixedSelector{AgentConnectionTarget{
		Local: true, AgentID: "agent-a", ConnectionID: "conn-a", ConnectionEpoch: 3,
	}}, local, nil)
	if _, err := transport.OpenStream(context.Background(), StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22}); err != nil {
		t.Fatal(err)
	}
	if local.request.NodeID != "" || local.request.TargetConnectionID != "conn-a" || local.request.TargetConnectionEpoch != 3 {
		t.Fatalf("local request=%+v, want connection-pinned local request", local.request)
	}
}

func TestSelectedTransportRoutesRemoteConnection(t *testing.T) {
	remote := &recordingTransport{}
	nodes := NewRelayService()
	nodes.RegisterNode("server-b", 7, remote)
	transport := NewSelectedTransport(fixedSelector{AgentConnectionTarget{
		AgentID: "agent-a", ConnectionID: "conn-b", ConnectionEpoch: 4,
		ServerNodeID: "server-b", ServerNodeEpoch: 7,
	}}, nil, nodes)
	if _, err := transport.OpenStream(context.Background(), StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.2", TargetPort: 80}); err != nil {
		t.Fatal(err)
	}
	if remote.request.NodeID != "server-b" || remote.request.Epoch != 7 ||
		remote.request.TargetConnectionID != "conn-b" || remote.request.TargetConnectionEpoch != 4 {
		t.Fatalf("remote request=%+v, want server and connection identity", remote.request)
	}
}
