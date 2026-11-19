package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
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

// `client.stream.inbound_buffer_bytes` must reach both per-stream queues, which
// is the only place the Client bounds what the Server may send it.
func TestClientInboundBufferComesFromConfiguration(t *testing.T) {
	transport := &receiveTransport{sent: make(chan protocol.Frame, 1), recv: make(chan protocol.Frame, 1), done: make(chan struct{})}
	session := NewSession(transport)
	defer session.Close()
	if err := session.SetInboundBufferBytes(protocol.MaxStreamFrame); !errors.Is(err, ErrInboundBufferTooSmall) {
		t.Fatalf("a one-frame queue cannot hold the advertised credit: err=%v", err)
	}
	if err := session.SetInboundBufferBytes(131072); err != nil {
		t.Fatal(err)
	}
	if got := newFrameStream(session, 7).readQueue.Capacity(); got != 131072 {
		t.Fatalf("byte stream queue capacity = %d, want the configured 131072", got)
	}
	if got := newFrameDatagramStream(session, 8).readQueue.Capacity(); got != 131072 {
		t.Fatalf("datagram stream queue capacity = %d, want the configured 131072", got)
	}
}

func TestSessionEndsWhenServerStopsAnsweringHeartbeats(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 16), recv: make(chan protocol.Frame, 4), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	session.livenessInterval = 10 * time.Millisecond
	session.Start()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Wait(waitCtx); !errors.Is(err, ErrPeerUnresponsive) {
		t.Fatalf("Wait() = %v, want ErrPeerUnresponsive", err)
	}
}

func TestSessionStaysUpWhileServerAnswersHeartbeats(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 64), recv: make(chan protocol.Frame, 64), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	session.livenessInterval = 20 * time.Millisecond
	session.Start()
	defer session.Close()
	// Answer every heartbeat for well over the 3-interval liveness budget.
	stop := time.After(300 * time.Millisecond)
	for {
		select {
		case frame := <-tr.sent:
			if frame.Type == protocol.FramePing {
				tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: frame.Payload}
			}
		case <-stop:
			select {
			case <-session.done:
				t.Fatalf("session ended while the server was answering: %v", session.runErr)
			default:
			}
			return
		}
	}
}

func clientAckFrame(t *testing.T, ack protocol.ClientMetadataAckPayload) protocol.Frame {
	t.Helper()
	payload, err := protocol.EncodeClientMetadataAckPayload(ack)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameClientMetadataAck, Payload: payload}
}

// The Server version in the metadata ack is the ceiling for the client's
// self-upgrade, so the session has to keep it instead of dropping the frame.
func TestSessionRecordsTheServerVersionFromTheMetadataAck(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 4), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenMetadata)
	defer session.Close()
	session.Start()
	if got := session.ServerVersion(); got != "" {
		t.Fatalf("ServerVersion() = %q before any ack, want empty", got)
	}
	tr.recv <- clientAckFrame(t, protocol.ClientMetadataAckPayload{Accepted: true, ServerVersion: "v1.4.0"})
	waitForCondition(t, time.Second, func() bool { return session.ServerVersion() == "v1.4.0" })
}

func TestSessionIgnoresTheVersionInARejectedOrVersionlessAck(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 4), recv: make(chan protocol.Frame, 8), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenMetadata)
	defer session.Close()
	session.Start()
	tr.recv <- clientAckFrame(t, protocol.ClientMetadataAckPayload{Accepted: false, ServerVersion: "v9.9.9"})
	tr.recv <- clientAckFrame(t, protocol.ClientMetadataAckPayload{Accepted: true})
	// A later accepted ack with a version still lands, which proves the earlier
	// frames were processed rather than never read.
	tr.recv <- clientAckFrame(t, protocol.ClientMetadataAckPayload{Accepted: true, ServerVersion: "v1.2.0"})
	waitForCondition(t, time.Second, func() bool { return session.ServerVersion() == "v1.2.0" })
}

// gatheredCounter reads one counter without depending on unexported collector
// fields, which is what lets client tests assert observability behaviour.
func gatheredCounter(t *testing.T, reg *prometheus.Registry, familyName string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != familyName {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := true
			for name, want := range labels {
				found := false
				for _, pair := range metric.GetLabel() {
					if pair.GetName() == name && pair.GetValue() == want {
						found = true
						break
					}
				}
				if !found {
					matched = false
					break
				}
			}
			if matched {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// waitForFloat polls one counter until it reaches want, because the receive
// loop publishes the observation asynchronously from the test goroutine.
func waitForFloat(t *testing.T, read func() float64, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := read(); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("metric never reached %v (last %v)", want, read())
}

func TestSessionAttributesDataBytesToTunnel(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)
	tr := &receiveTransport{sent: make(chan protocol.Frame, 16), recv: make(chan protocol.Frame, 16), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	session.Metrics = metrics
	defer session.Close()

	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{
		AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 443, Tunnel: "edge-https",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var streamID uint32
	for streamID == 0 {
		select {
		case frame := <-tr.sent:
			if frame.Type == protocol.FrameOpenStream {
				streamID = frame.StreamID
			}
		case <-time.After(2 * time.Second):
			t.Fatal("OPEN_STREAM was not sent")
		}
	}

	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-tr.sent:
		if frame.Type != protocol.FrameData || frame.StreamID != streamID || len(frame.Payload) != 5 {
			t.Fatalf("outbound frame = %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DATA was not sent")
	}
	outbound := map[string]string{"component": "client", "tunnel": "edge-https", "direction": "outbound", "protocol": "tcp"}
	if got := gatheredCounter(t, reg, "tunnelmesh_client_stream_bytes_total", outbound); got != 5 {
		t.Fatalf("outbound tunnel bytes = %v, want 5", got)
	}
	if got := gatheredCounter(t, reg, "tunnelmesh_client_frames_total", map[string]string{
		"component": "client", "type": "data", "direction": "outbound",
	}); got != 1 {
		t.Fatalf("outbound data frames = %v, want 1", got)
	}

	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: streamID, Payload: []byte("world!")}
	inbound := map[string]string{"component": "client", "tunnel": "edge-https", "direction": "inbound", "protocol": "tcp"}
	waitForFloat(t, func() float64 {
		return gatheredCounter(t, reg, "tunnelmesh_client_stream_bytes_total", inbound)
	}, 6)
}

func TestSessionCountsDroppedDataBytes(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)
	tr := &receiveTransport{sent: make(chan protocol.Frame, 32), recv: make(chan protocol.Frame, 32), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	session.Metrics = metrics
	defer session.Close()
	session.Start()

	dropped := func(reason string) float64 {
		return gatheredCounter(t, reg, "tunnelmesh_client_dropped_bytes_total", map[string]string{
			"component": "client", "reason": reason,
		})
	}

	// An unknown stream models the production case: the peer keeps sending on a
	// stream this client already forgot, so every byte is acknowledged and lost.
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 4242, Payload: make([]byte, 100)}
	waitForFloat(t, func() float64 { return dropped("unknown_stream") }, 100)

	openStream := func(port int) (io.ReadWriteCloser, uint32) {
		stream, err := session.OpenStreamConn(context.Background(), StreamRequest{
			AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: port,
		})
		if err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case frame := <-tr.sent:
				if frame.Type == protocol.FrameOpenStream {
					return stream, frame.StreamID
				}
			case <-time.After(2 * time.Second):
				t.Fatal("OPEN_STREAM was not sent")
			}
		}
	}

	closedStream, closedID := openStream(443)
	defer closedStream.Close()
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: closedID}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: closedID, Payload: make([]byte, 64)}
	waitForFloat(t, func() float64 { return dropped("closed_stream") }, 64)

	exhausted, exhaustedID := openStream(8443)
	defer exhausted.Close()
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: exhaustedID, Payload: make([]byte, 300<<10)}
	waitForFloat(t, func() float64 { return dropped("window_or_queue") }, 300<<10)
}

// TestSessionStartIsIdempotent pins the fix for a bandwidth leak: Start is
// called from every OpenStreamConn, so unguarded metadata work sent a full
// CLIENT_HELLO per proxied connection and leaked one 30s ticker per call.
func TestSessionStartIsIdempotent(t *testing.T) {
	collector, err := NewClientMetadataCollector(ClientMetadataOptions{InstanceID: "client-0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	tr := &receiveTransport{sent: make(chan protocol.Frame, 8), recv: make(chan protocol.Frame, 8), done: make(chan struct{})}
	session := NewSessionWithOpenModeAndMetadata(tr, SessionOpenMetadata, collector)
	defer session.Close()

	session.Start()
	session.Start()
	stream, err := session.OpenStreamConn(context.Background(), StreamRequest{AgentID: "agent", Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if first := <-tr.sent; first.Type != protocol.FrameClientHello {
		t.Fatalf("first frame = %v, want CLIENT_HELLO", first.Type)
	}
	if second := <-tr.sent; second.Type != protocol.FrameOpenStream {
		t.Fatalf("second frame = %v, want OPEN_STREAM", second.Type)
	}
	select {
	case extra := <-tr.sent:
		t.Fatalf("unexpected extra frame after one session start: %v", extra.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

func newMetadataTestSession(t *testing.T) (*Session, *receiveTransport) {
	t.Helper()
	collector, err := NewClientMetadataCollector(ClientMetadataOptions{InstanceID: "client-0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	tr := &receiveTransport{sent: make(chan protocol.Frame, 8), recv: make(chan protocol.Frame, 8), done: make(chan struct{})}
	session := NewSessionWithOpenModeAndMetadata(tr, SessionOpenMetadata, collector)
	t.Cleanup(func() { _ = session.Close() })
	session.Start()
	select {
	case frame := <-tr.sent:
		if frame.Type != protocol.FrameClientHello {
			t.Fatalf("first frame = %v, want CLIENT_HELLO", frame.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("CLIENT_HELLO was not sent")
	}
	return session, tr
}

func TestSessionMetadataUpdateSkipsUnchangedWithinKeepalive(t *testing.T) {
	session, tr := newMetadataTestSession(t)
	if err := session.sendClientMetadata(protocol.FrameClientMetadataUpdate); err != nil {
		t.Fatal(err)
	}
	if err := session.sendClientMetadata(protocol.FrameClientMetadataUpdate); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-tr.sent:
		t.Fatalf("unchanged metadata was resent: %v", frame.Type)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSessionMetadataUpdateResendsAfterKeepalive(t *testing.T) {
	session, tr := newMetadataTestSession(t)
	session.mu.Lock()
	session.metadataSentAt = time.Now().Add(-clientMetadataKeepalive - time.Second)
	session.mu.Unlock()
	if err := session.sendClientMetadata(protocol.FrameClientMetadataUpdate); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-tr.sent:
		if frame.Type != protocol.FrameClientMetadataUpdate {
			t.Fatalf("frame = %v, want CLIENT_METADATA_UPDATE", frame.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("metadata keepalive was not sent")
	}
}
