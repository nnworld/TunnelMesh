package client

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestSessionSlowStreamDoesNotBlockAnotherStreamWrite(t *testing.T) {
	tr := newBlockingDataTransport()
	session := NewSession(tr)
	defer session.Close()
	first, err := session.OpenStreamConn(context.Background(), StreamRequest{AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.OpenStreamConn(context.Background(), StreamRequest{AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 2})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		_, _ = first.Write([]byte("slow-data"))
	}()
	select {
	case <-tr.dataStarted:
	case <-time.After(time.Second):
		t.Fatal("slow stream did not reach the transport")
	}

	done := make(chan error, 1)
	go func() {
		_, err := second.Write([]byte("fast-data"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fast stream write error=%v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("slow stream blocked another stream write")
	}
	close(tr.release)
	_ = first.Close()
	_ = second.Close()
}

func TestSessionSlowStreamDoesNotBlockPing(t *testing.T) {
	tr := newBlockingDataTransport()
	session := NewSession(tr)
	defer session.Close()
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	for i := 0; i < 20; i++ {
		tr.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 1, Payload: []byte("data")}
	}
	tr.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("ping")}

	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case frame := <-tr.sent:
			if frame.Type == protocol.FramePong {
				return
			}
		case <-deadline:
			t.Fatal("slow stream blocked connection-level PING")
		}
	}
}

func TestSessionPingMatchesCorrelationID(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 1), recv: make(chan protocol.Frame, 1), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	defer session.Close()
	session.Start()

	go func() {
		select {
		case frame := <-tr.sent:
			tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: frame.Payload}
		case <-time.After(time.Second):
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rtt, err := session.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rtt < 0 {
		t.Fatalf("Ping() RTT=%v, want non-negative", rtt)
	}
}

func TestSessionSlowDatagramDoesNotBlockPing(t *testing.T) {
	tr := newBlockingDataTransport()
	session := NewSession(tr)
	defer session.Close()
	datagram, err := session.OpenDatagram(context.Background(), StreamRequest{AgentID: "agent", Protocol: "udp", TargetHost: "host", TargetPort: 53})
	if err != nil {
		t.Fatal(err)
	}
	defer datagram.Close()
	streamID := uint32(1)

	for i := 0; i < 20; i++ {
		tr.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: streamID, Payload: []byte("datagram")}
	}
	tr.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("ping")}

	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case frame := <-tr.sent:
			if frame.Type == protocol.FramePong {
				return
			}
		case <-deadline:
			t.Fatal("slow datagram blocked connection-level PING")
		}
	}
}

func TestSessionFlowControlAdvertisesWindowAndWaitsForCredit(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 4), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	if session.OpenMode() != SessionOpenFlowControl {
		t.Fatalf("OpenMode()=%v, want flow control", session.OpenMode())
	}
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{StreamID: 7, AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	select {
	case open := <-tr.sent:
		if open.Type != protocol.FrameOpenStream || open.Window != 262144 {
			t.Fatalf("OPEN frame=%+v, want advertised window 262144", open)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OPEN")
	}
	// A write larger than the outstanding credit must wait for the peer to
	// grant more, not fail. Failing is what truncated bulk transfers: callers
	// copy through this stream with io.Copy and treat the first error as the
	// end of the body, while the peer keeps the Content-Length it was promised.
	written := make(chan int, 1)
	writeErr := make(chan error, 1)
	go func() {
		n, err := stream.Write(make([]byte, 262145))
		written <- n
		writeErr <- err
	}()

	var drained int
	for drained < 262144 {
		select {
		case frame := <-tr.sent:
			if frame.Type != protocol.FrameData {
				continue
			}
			// One frame must never exceed the documented per-frame cap: the
			// peer's inbound budget is smaller than the encoder limit, so an
			// oversized frame would be refused and kill a healthy stream.
			if len(frame.Payload) > protocol.MaxStreamFrame {
				t.Fatalf("DATA payload = %d bytes, want at most %d", len(frame.Payload), protocol.MaxStreamFrame)
			}
			drained += len(frame.Payload)
		case err := <-writeErr:
			t.Fatalf("write stopped after %d bytes with err=%v, want it to wait for credit", drained, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out after %d bytes", drained)
		}
	}
	select {
	case err := <-writeErr:
		t.Fatalf("write finished before the last byte was credited: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 7, Window: 1}
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("write after WINDOW_UPDATE failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write never resumed after the peer granted credit")
	}
	if n := <-written; n != 262145 {
		t.Fatalf("Write returned %d, want 262145", n)
	}
}

func TestSessionOpenModeNegotiatesClientMetadata(t *testing.T) {
	legacy := &receiveTransport{sent: make(chan protocol.Frame, 1), recv: make(chan protocol.Frame, 1), done: make(chan struct{})}
	if got := sessionOpenModeForTransport(legacy); got != SessionOpenLegacy {
		t.Fatalf("legacy OpenMode()=%v, want legacy", got)
	}

	metadata := &metadataReceiveTransport{receiveTransport: legacy}
	if got := sessionOpenModeForTransport(metadata); got != SessionOpenMetadata {
		t.Fatalf("metadata OpenMode()=%v, want metadata", got)
	}
}

func TestSessionSendsClientHelloBeforeOpenStream(t *testing.T) {
	tr := &metadataReceiveTransport{receiveTransport: &receiveTransport{
		sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 4), done: make(chan struct{}),
	}}
	collector, err := NewClientMetadataCollector(ClientMetadataOptions{InstanceID: "client-0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	session := NewSessionWithOpenModeAndMetadata(tr, SessionOpenMetadata, collector)
	defer session.Close()
	session.Start()

	if err := session.OpenStream(context.Background(), StreamRequest{StreamID: 1, AgentID: "agent", Protocol: "tcp", TargetHost: "example", TargetPort: 80}); err != nil {
		t.Fatal(err)
	}

	first := <-tr.sent
	if first.Type != protocol.FrameClientHello {
		t.Fatalf("first frame type=%v, want CLIENT_HELLO", first.Type)
	}
	payload, err := protocol.DecodeClientMetadataPayload(first.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.InstanceID != "client-0123456789abcdef0123456789abcdef" || payload.Revision != 1 {
		t.Fatalf("CLIENT_HELLO payload=%#v", payload)
	}
	second := <-tr.sent
	if second.Type != protocol.FrameOpenStream {
		t.Fatalf("second frame type=%v, want OPEN", second.Type)
	}
	if second.Window == 0 {
		t.Fatal("metadata mode did not retain flow-control window")
	}
}

type metadataReceiveTransport struct {
	*receiveTransport
}

func (t *metadataReceiveTransport) Subprotocol() string {
	return protocol.SubprotocolClientMetadata
}

func TestSessionFlowControlSendsThresholdWindowUpdate(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 4), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{StreamID: 8, AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-tr.sent:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OPEN")
	}

	payload := make([]byte, 131072)
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 8, Payload: payload}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buffer); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-tr.sent:
		if update.Type != protocol.FrameWindowUpdate || update.StreamID != 8 || update.Window != 131072 {
			t.Fatalf("WINDOW_UPDATE=%+v, want 131072 bytes for stream 8", update)
		}
	case <-time.After(time.Second):
		t.Fatal("threshold WINDOW_UPDATE was not sent")
	}
}

func TestSessionFlowControlWaitsForApplicationReadBeforeWindowUpdate(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 4), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{StreamID: 10, AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-tr.sent

	payload := make([]byte, 131072)
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 10, Payload: payload}
	if _, err := io.ReadFull(stream, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	select {
	case frame := <-tr.sent:
		t.Fatalf("WINDOW_UPDATE before application consumed bytes: %+v", frame)
	default:
	}

	remaining := make([]byte, len(payload)-1)
	if _, err := io.ReadFull(stream, remaining); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-tr.sent:
		if update.Type != protocol.FrameWindowUpdate || update.StreamID != 10 || update.Window != uint32(len(payload)) {
			t.Fatalf("WINDOW_UPDATE=%+v, want %d bytes for stream 10", update, len(payload))
		}
	case <-time.After(time.Second):
		t.Fatal("threshold WINDOW_UPDATE was not sent after application consumed the frame")
	}
}

func TestSessionFlowControlAppliesPeerWindowUpdate(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 4), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{StreamID: 9, AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-tr.sent
	if _, err := stream.Write(make([]byte, 262144)); err != nil {
		t.Fatal(err)
	}
	for remaining := 262144; remaining > 0; {
		frame := <-tr.sent
		if frame.Type != protocol.FrameData || frame.StreamID != 9 {
			t.Fatalf("initial DATA=%+v", frame)
		}
		remaining -= len(frame.Payload)
	}

	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 9, Window: 100}
	time.Sleep(20 * time.Millisecond)
	if _, err := stream.Write(make([]byte, 100)); err != nil {
		t.Fatalf("write after WINDOW_UPDATE failed: %v", err)
	}
	select {
	case frame := <-tr.sent:
		if frame.Type != protocol.FrameData || frame.StreamID != 9 || len(frame.Payload) != 100 {
			t.Fatalf("post-update DATA=%+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("post-update DATA was not sent")
	}
}

type blockingDataTransport struct {
	mu          sync.Mutex
	sent        chan protocol.Frame
	receive     chan protocol.Frame
	dataStarted chan struct{}
	release     chan struct{}
	closed      chan struct{}
	once        sync.Once
}

func newBlockingDataTransport() *blockingDataTransport {
	return &blockingDataTransport{
		sent: make(chan protocol.Frame, 32), receive: make(chan protocol.Frame, 64),
		dataStarted: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (t *blockingDataTransport) Send(frame protocol.Frame) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if frame.Type == protocol.FrameData {
		t.once.Do(func() { close(t.dataStarted) })
		<-t.release
	}
	select {
	case t.sent <- frame:
		return nil
	default:
		return io.ErrClosedPipe
	}
}

func (t *blockingDataTransport) Receive() (protocol.Frame, error) {
	select {
	case frame := <-t.receive:
		return frame, nil
	case <-t.closed:
		return protocol.Frame{}, io.EOF
	}
}

func (t *blockingDataTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

// TestSessionFlowControlBulkWriteIsDeliveredIntact covers the user-visible
// symptom: a body several times larger than one window must arrive complete and
// in order. The peer releases credit the way the Server does, so the write is
// limited by the protocol rather than by a queue that refuses frames.
func TestSessionFlowControlBulkWriteIsDeliveredIntact(t *testing.T) {
	const total = 1 << 20 // four times the credit a single window grants
	tr := &receiveTransport{sent: make(chan protocol.Frame, 64), recv: make(chan protocol.Frame, 64), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	defer session.Close()
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{StreamID: 11, AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-tr.sent // OPEN_STREAM

	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i * 7)
	}

	received := make(chan []byte, 128)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		var got, unacked int
		for frame := range tr.sent {
			if frame.Type != protocol.FrameData || frame.StreamID != 11 {
				continue
			}
			received <- append([]byte(nil), frame.Payload...)
			got += len(frame.Payload)
			unacked += len(frame.Payload)
			if unacked >= 131072 {
				tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 11, Window: uint32(unacked)}
				unacked = 0
			}
			if got >= total {
				return
			}
		}
	}()

	writeErr := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		writeErr <- err
	}()

	var got []byte
	writeDone := false
	deadline := time.After(30 * time.Second)
	for len(got) < total {
		select {
		case chunk := <-received:
			got = append(got, chunk...)
		case err := <-writeErr:
			writeDone = true
			if err != nil {
				t.Fatalf("bulk write failed after %d of %d bytes: %v", len(got), total, err)
			}
		case <-deadline:
			t.Fatalf("timed out with %d of %d bytes delivered", len(got), total)
		}
	}
	if !writeDone {
		select {
		case err := <-writeErr:
			if err != nil {
				t.Fatalf("bulk write error=%v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bulk write never returned")
		}
	}
	<-drained
	if !bytes.Equal(got, payload) {
		t.Fatalf("delivered %d bytes that differ from the payload", len(got))
	}
}
