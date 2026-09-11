package session

import (
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestBoundedFrameQueueRejectsWhenByteLimitReached(t *testing.T) {
	queue := NewBoundedFrameQueue(10)
	if !queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("12345")}) {
		t.Fatal("first frame was rejected")
	}
	if queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("123456")}) {
		t.Fatal("frame exceeding byte limit was accepted")
	}
	if !queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("12345")}) {
		t.Fatal("frame filling the queue was rejected")
	}
	if queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("1")}) {
		t.Fatal("frame after byte limit was accepted")
	}

	first, ok := queue.TryPop()
	if !ok || string(first.Payload) != "12345" {
		t.Fatalf("first=%+v ok=%v", first, ok)
	}
	second, ok := queue.TryPop()
	if !ok || string(second.Payload) != "12345" {
		t.Fatalf("second=%+v ok=%v", second, ok)
	}
	if _, ok := queue.TryPop(); ok {
		t.Fatal("empty queue returned a frame")
	}
}

func TestBoundedFrameQueueCopiesPayload(t *testing.T) {
	queue := NewBoundedFrameQueue(16)
	payload := []byte("original")
	if !queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: payload}) {
		t.Fatal("frame was rejected")
	}
	payload[0] = 'X'
	frame, ok := queue.TryPop()
	if !ok || string(frame.Payload) != "original" {
		t.Fatalf("payload=%q ok=%v, want original", frame.Payload, ok)
	}
}

func TestBoundedFrameQueueCloseDrainsAndStops(t *testing.T) {
	queue := NewBoundedFrameQueue(16)
	if !queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("data")}) {
		t.Fatal("frame was rejected")
	}
	queue.Close()
	if queue.TryPush(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("more")}) {
		t.Fatal("closed queue accepted a frame")
	}
	if _, ok := queue.TryPop(); ok {
		t.Fatal("closed queue returned a queued frame")
	}
}
