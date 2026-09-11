package client

import (
	"context"
	"errors"
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

func TestSessionFlowControlRejectsOverSendAndAdvertisesWindow(t *testing.T) {
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
	if _, err := stream.Write(make([]byte, 262145)); !errors.Is(err, protocol.ErrWindowExhausted) {
		t.Fatalf("over-send error=%v, want ErrWindowExhausted", err)
	}
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
	if frame := <-tr.sent; frame.Type != protocol.FrameData || frame.StreamID != 9 {
		t.Fatalf("initial DATA=%+v", frame)
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
