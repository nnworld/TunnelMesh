package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

// fakeExecStream plays the agent side of an exec stream: it reads the request, then
// emits the records a real run would produce.
type fakeExecStream struct {
	request   bytes.Buffer
	records   bytes.Buffer
	closed    bool
	onRequest func(script string)
}

func (s *fakeExecStream) Write(p []byte) (int, error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := s.request.Write(p)
	if err == nil && s.onRequest != nil && s.request.Len() > 4 {
		header, consumed, perr := protocol.ParseExecHeader(s.request.Bytes())
		if perr == nil && consumed > 0 && s.request.Len() >= consumed+header.ScriptBytes {
			script := s.request.Bytes()[consumed : consumed+header.ScriptBytes]
			s.onRequest(string(script))
			_ = protocol.WriteExecRecord(&s.records, protocol.ExecRecordStdout, []byte("ran\n"))
			exit, _ := json.Marshal(protocol.ExecExit{ExitCode: 0, DurationMS: 5})
			_ = protocol.WriteExecRecord(&s.records, protocol.ExecRecordExit, exit)
		}
	}
	return n, err
}

func (s *fakeExecStream) Read(p []byte) (int, error) {
	if s.records.Len() == 0 {
		return 0, io.EOF
	}
	return s.records.Read(p)
}

func (s *fakeExecStream) Close() error { s.closed = true; return nil }

type fakeExecOpener struct {
	stream *fakeExecStream
	req    struct{ Agent, Protocol string }
}

func (o *fakeExecOpener) OpenStream(_ context.Context, req relay.StreamRequest) (io.ReadWriteCloser, error) {
	o.req.Agent, o.req.Protocol = req.AgentID, req.Protocol
	return o.stream, nil
}

// fakeExecWS is the browser end of the WebSocket: it sends one run request and records
// every JSON frame the server sends back.
type fakeExecWS struct {
	incoming [][]byte
	sent     []map[string]any
	closed   bool
}

func (w *fakeExecWS) ReadMessage() (int, []byte, error) {
	if len(w.incoming) == 0 {
		return 0, nil, io.EOF
	}
	next := w.incoming[0]
	w.incoming = w.incoming[1:]
	return 1, next, nil
}

func (w *fakeExecWS) WriteMessage(_ int, data []byte) error {
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		return err
	}
	w.sent = append(w.sent, frame)
	return nil
}

func (w *fakeExecWS) Close() error { w.closed = true; return nil }

func TestAgentExecTicketIsSingleUseAndExpires(t *testing.T) {
	tickets := newAgentExecTickets(time.Now)
	created, err := tickets.issue("agent-1", "admin-1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.redeem(created.SessionID, created.Ticket); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := tickets.redeem(created.SessionID, created.Ticket); !errors.Is(err, errAgentExecTicketInvalid) {
		t.Fatalf("second redeem err = %v, want errAgentExecTicketInvalid", err)
	}

	clock := time.Now()
	expiring := newAgentExecTickets(func() time.Time { return clock })
	issued, _ := expiring.issue("agent-1", "admin-1", 30*time.Second)
	clock = clock.Add(31 * time.Second)
	if _, err := expiring.redeem(issued.SessionID, issued.Ticket); !errors.Is(err, errAgentExecTicketInvalid) {
		t.Fatalf("expired ticket err = %v", err)
	}
}

func TestAgentExecTicketStoresOnlyItsHash(t *testing.T) {
	tickets := newAgentExecTickets(time.Now)
	created, _ := tickets.issue("agent-1", "admin-1", time.Minute)
	for _, stored := range tickets.entries {
		if strings.Contains(stored.hash, created.Ticket) || len(stored.hash) != 64 {
			t.Fatalf("stored value must be a SHA-256 hex digest, got %q", stored.hash)
		}
	}
}

func TestAgentExecBridgeRunsScriptAndForwardsRecordsAsJSON(t *testing.T) {
	var audited []AgentExecAuditEvent
	stream := &fakeExecStream{}
	ran := ""
	stream.onRequest = func(script string) { ran = script }
	opener := &fakeExecOpener{stream: stream}
	broker := newAgentExecBroker(agentExecBrokerDeps{Opener: opener, Audit: func(_ context.Context, e AgentExecAuditEvent) { audited = append(audited, e) }})
	created, err := broker.tickets.issue("agent-1", "admin-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ws := &fakeExecWS{incoming: [][]byte{[]byte(`{"type":"run","script":"echo ran","timeoutSec":10}`)}}
	if err := broker.Handle(context.Background(), ws, created.SessionID, created.Ticket); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if ran != "echo ran" {
		t.Fatalf("agent received script %q", ran)
	}
	if opener.req.Protocol != protocol.StreamProtocolExec || opener.req.Agent != "agent-1" {
		t.Fatalf("opened %+v", opener.req)
	}
	var sawStdout, sawExit bool
	for _, frame := range ws.sent {
		switch frame["type"] {
		case "stdout":
			raw, _ := base64.StdEncoding.DecodeString(frame["data"].(string))
			sawStdout = string(raw) == "ran\n"
		case "exit":
			sawExit = frame["exitCode"] == float64(0)
		}
	}
	if !sawStdout || !sawExit {
		t.Fatalf("frames = %+v", ws.sent)
	}
	if len(audited) != 2 || audited[0].Action != "agent.exec.started" || audited[1].Action != "agent.exec.finished" {
		t.Fatalf("audit = %+v, want started then finished", audited)
	}
	for _, event := range audited {
		if strings.Contains(event.Details, "echo ran") {
			t.Fatalf("audit must not contain script text: %s", event.Details)
		}
	}
	if !ws.closed {
		t.Fatal("bridge must close the browser socket when the run ends")
	}
}

func TestAgentExecBridgeRejectsOversizedScript(t *testing.T) {
	broker := newAgentExecBroker(agentExecBrokerDeps{Opener: &fakeExecOpener{stream: &fakeExecStream{}}, MaxScriptBytes: 8})
	created, _ := broker.tickets.issue("agent-1", "admin-1", time.Minute)
	ws := &fakeExecWS{incoming: [][]byte{[]byte(`{"type":"run","script":"echo way too long","timeoutSec":10}`)}}
	if err := broker.Handle(context.Background(), ws, created.SessionID, created.Ticket); err == nil {
		t.Fatal("an oversized script must be refused before any stream opens")
	}
}

func decodeFrameData(t *testing.T, value any) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(value.(string))
	if err != nil {
		t.Fatalf("frame data is not base64: %v", err)
	}
	return string(raw)
}

// A script run has no target. The relay must accept an exec stream without host or port
// (ICMP and TCP keep their checks), and the connection selector must route an exec
// stream only to a session that negotiated agent_exec.v1.
func TestExecStreamNeedsNoTargetButKeepsTheCapabilityGate(t *testing.T) {
	if got := streamProtocolCapability(protocol.StreamProtocolExec); got != protocol.CapabilityAgentExec {
		t.Fatalf("exec must require %s from the selector, got %q", protocol.CapabilityAgentExec, got)
	}
	transport := NewAgentRelayTransport(NewAgentSessionManager(AgentSessionConfig{}), AgentRelayWindowConfig{})
	_, err := transport.OpenStream(context.Background(), relay.StreamRequest{AgentID: "nobody", Protocol: protocol.StreamProtocolExec})
	if err == nil || errors.Is(err, relay.ErrNodeDisconnected) && strings.Contains(err.Error(), "target") {
		t.Fatalf("exec with no target must get past the target check, err=%v", err)
	}
	if _, err := transport.OpenStream(context.Background(), relay.StreamRequest{AgentID: "nobody", Protocol: "tcp"}); !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("tcp without a target must still be refused, err=%v", err)
	}
}
