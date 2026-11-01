package protocol

import (
	"net"
	"sync"
	"testing"
	"time"
)

func streamPair(t *testing.T) (*StreamConn, *StreamConn) {
	t.Helper()
	a, b := net.Pipe()
	left := NewStreamConn(a, 2*time.Second)
	right := NewStreamConn(b, 2*time.Second)
	// A bounded receive turns a lost frame into a fast failure instead of a hang.
	right.SetIdleTimeout(2 * time.Second)
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	return left, right
}

func TestStreamConnRoundTripsFrames(t *testing.T) {
	left, right := streamPair(t)
	want := Frame{Version: CurrentVersion, Type: FrameData, StreamID: 3, Payload: []byte("hello")}
	// net.Pipe is synchronous: the write only completes once the peer reads, so the
	// send must run concurrently with the receive.
	sendErr := make(chan error, 1)
	go func() { sendErr <- left.Send(want) }()
	got, err := right.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.Type != want.Type || got.StreamID != want.StreamID || string(got.Payload) != "hello" {
		t.Fatalf("got %+v", got)
	}
}

func TestStreamConnSerialisesConcurrentSenders(t *testing.T) {
	left, right := streamPair(t)
	const senders, each = 8, 16
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := left.Send(Frame{Version: CurrentVersion, Type: FrameData, StreamID: uint32(s + 1), Payload: []byte{byte(i)}}); err != nil {
					t.Errorf("send: %v", err)
					return
				}
			}
		}(s)
	}
	go func() { wg.Wait() }()
	for i := 0; i < senders*each; i++ {
		if _, err := right.Receive(); err != nil {
			t.Fatalf("frame %d interleaved or corrupt: %v", i, err)
		}
	}
}

func TestStreamConnIdleTimeoutEndsSilentPeer(t *testing.T) {
	_, right := streamPair(t)
	right.SetIdleTimeout(50 * time.Millisecond)
	start := time.Now()
	if _, err := right.Receive(); err == nil {
		t.Fatal("a silent peer must hit the idle deadline")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("idle deadline did not fire promptly")
	}
}

func TestStreamConnWriteTimeoutClosesBlockedPeer(t *testing.T) {
	a, _ := net.Pipe() // nothing reads the other end, so the write blocks
	conn := NewStreamConn(a, 50*time.Millisecond)
	if err := conn.Send(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 1, Payload: []byte("stuck")}); err == nil {
		t.Fatal("a blocked write must time out")
	}
	if err := conn.Send(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 1}); err == nil {
		t.Fatal("a stream closed by a failed write must refuse later sends")
	}
}

func TestStreamConnCloseIsIdempotent(t *testing.T) {
	left, _ := streamPair(t)
	if err := left.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	_ = left.Close()
	if err := left.Send(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 1}); err == nil {
		t.Fatal("send after close must fail")
	}
}
