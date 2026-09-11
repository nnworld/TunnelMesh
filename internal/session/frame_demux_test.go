package session

import (
	"errors"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestFrameDemuxRoutesControlAndStreamFrames(t *testing.T) {
	control := make(chan protocol.Frame, 1)
	demux := NewFrameDemux(32, func(frame protocol.Frame) error {
		control <- frame
		return nil
	})
	defer demux.Close()

	if err := demux.Dispatch(protocol.Frame{Type: protocol.FramePing, Payload: []byte("ping")}); err != nil {
		t.Fatal(err)
	}
	if err := demux.Dispatch(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if err := demux.Dispatch(protocol.Frame{Type: protocol.FrameData, StreamID: 2, Payload: []byte("two")}); err != nil {
		t.Fatal(err)
	}

	select {
	case frame := <-control:
		if frame.Type != protocol.FramePing || string(frame.Payload) != "ping" {
			t.Fatalf("control frame=%+v", frame)
		}
	default:
		t.Fatal("control frame was not dispatched")
	}
	first, ok := demux.Stream(1).TryPop()
	if !ok || string(first.Payload) != "one" {
		t.Fatalf("stream 1 frame=%+v ok=%v", first, ok)
	}
	second, ok := demux.Stream(2).TryPop()
	if !ok || string(second.Payload) != "two" {
		t.Fatalf("stream 2 frame=%+v ok=%v", second, ok)
	}
}

func TestFrameDemuxQueueFullOnlyAffectsCurrentStream(t *testing.T) {
	demux := NewFrameDemux(4, func(protocol.Frame) error { return nil })
	defer demux.Close()
	for i := 0; i < 4; i++ {
		if err := demux.Dispatch(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("a")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := demux.Dispatch(protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("b")}); !errors.Is(err, ErrStreamQueueFull) {
		t.Fatalf("queue-full error=%v, want ErrStreamQueueFull", err)
	}
	if err := demux.Dispatch(protocol.Frame{Type: protocol.FrameData, StreamID: 2, Payload: []byte("ok")}); err != nil {
		t.Fatalf("other stream was affected: %v", err)
	}
}
