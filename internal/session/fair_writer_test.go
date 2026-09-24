package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestFairFrameWriterSendsControlBeforeDataAndFairlyBetweenStreams(t *testing.T) {
	sent := make(chan protocol.Frame, 16)
	writer := NewFairFrameWriter(func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, FairWriterConfig{ControlQueueSize: 8, StreamQueueBytes: 1024, QuantumBytes: 32})

	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("bulk-1")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnqueueData(2, protocol.Frame{Type: protocol.FrameData, StreamID: 2, Payload: []byte("small")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("bulk-2")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnqueueControl(protocol.Frame{Type: protocol.FramePong}); err != nil {
		t.Fatal(err)
	}

	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { runDone <- writer.Run(ctx) }()
	defer func() {
		cancel()
		_ = writer.Close()
		select {
		case err := <-runDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run error=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Run did not stop")
		}
	}()

	expect := []struct {
		streamID  uint32
		frameType protocol.FrameType
	}{
		{0, protocol.FramePong},
		{2, protocol.FrameData},
		{1, protocol.FrameData},
	}
	for _, want := range expect {
		select {
		case frame := <-sent:
			if frame.StreamID != want.streamID || frame.Type != want.frameType {
				t.Fatalf("frame=%+v, want stream=%d type=%d", frame, want.streamID, want.frameType)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for stream %d type %d", want.streamID, want.frameType)
		}
	}
}

func TestFairFrameWriterQueueLimits(t *testing.T) {
	writer := NewFairFrameWriter(func(protocol.Frame) error { return nil }, FairWriterConfig{
		ControlQueueSize: 1, StreamQueueBytes: 2, QuantumBytes: 32,
	})
	defer writer.Close()

	if err := writer.EnqueueControl(protocol.Frame{Type: protocol.FramePong}); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnqueueControl(protocol.Frame{Type: protocol.FramePong}); !errors.Is(err, ErrControlQueueFull) {
		t.Fatalf("second control error=%v, want ErrControlQueueFull", err)
	}
	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("aa")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("b")}); !errors.Is(err, ErrStreamQueueFull) {
		t.Fatalf("second data error=%v, want ErrStreamQueueFull", err)
	}
	if err := writer.EnqueueData(2, protocol.Frame{Type: protocol.FrameData, StreamID: 2, Payload: []byte("c")}); err != nil {
		t.Fatalf("other stream was blocked by stream 1: %v", err)
	}
}

func TestFairFrameWriterPreservesHalfCloseAfterStreamData(t *testing.T) {
	sent := make(chan protocol.Frame, 2)
	writer := NewFairFrameWriter(func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, FairWriterConfig{ControlQueueSize: 4, StreamQueueBytes: 1024, QuantumBytes: 32})
	defer writer.Close()

	if err := writer.EnqueueData(7, protocol.Frame{Type: protocol.FrameData, StreamID: 7, Payload: []byte("request")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnqueueControl(protocol.Frame{Type: protocol.FrameHalfClose, StreamID: 7}); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- writer.Run(context.Background()) }()

	for i, want := range []protocol.FrameType{protocol.FrameData, protocol.FrameHalfClose} {
		select {
		case frame := <-sent:
			if frame.Type != want || frame.StreamID != 7 {
				t.Fatalf("frame %d=%+v, want stream 7 %v", i, frame, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for frame %d", i)
		}
	}
}

func TestFairFrameWriterEnqueueControlSyncWaitsForOwnFrame(t *testing.T) {
	sent := make(chan protocol.Frame, 4)
	dataStarted := make(chan struct{})
	release := make(chan struct{})
	first := true
	writer := NewFairFrameWriter(func(frame protocol.Frame) error {
		if frame.Type == protocol.FrameData && first {
			first = false
			close(dataStarted)
			<-release
		}
		sent <- frame
		return nil
	}, FairWriterConfig{ControlQueueSize: 4, StreamQueueBytes: 1024, QuantumBytes: 32})
	defer writer.Close()

	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("slow")}); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- writer.Run(context.Background()) }()
	select {
	case <-dataStarted:
	case <-time.After(time.Second):
		t.Fatal("blocked DATA did not reach sender")
	}
	if err := writer.EnqueueData(2, protocol.Frame{Type: protocol.FrameData, StreamID: 2, Payload: []byte("queued")}); err != nil {
		t.Fatal(err)
	}

	syncDone := make(chan error, 1)
	go func() {
		syncDone <- writer.EnqueueControlSync(protocol.Frame{Type: protocol.FramePong, Payload: []byte("ready")})
	}()
	select {
	case err := <-syncDone:
		t.Fatalf("sync control returned before its frame was sent: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	for i := 0; i < 2; i++ {
		select {
		case frame := <-sent:
			if i == 0 && (frame.Type != protocol.FrameData || frame.StreamID != 1) {
				t.Fatalf("first frame=%+v, want stream 1 DATA", frame)
			}
			if i == 1 && frame.Type != protocol.FramePong {
				t.Fatalf("second frame=%+v, want PONG", frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for frame %d", i)
		}
	}
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sync control did not return after its frame was sent")
	}
}

func TestFairFrameWriterTracksQueueWaitP95(t *testing.T) {
	dataStarted := make(chan struct{})
	release := make(chan struct{})
	first := true
	writer := NewFairFrameWriter(func(frame protocol.Frame) error {
		if frame.Type == protocol.FrameData && first {
			first = false
			close(dataStarted)
			<-release
		}
		return nil
	}, FairWriterConfig{ControlQueueSize: 4, StreamQueueBytes: 1024, QuantumBytes: 32})
	defer writer.Close()

	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- writer.Run(context.Background()) }()
	select {
	case <-dataStarted:
	case <-time.After(time.Second):
		t.Fatal("first DATA did not reach sender")
	}
	if err := writer.EnqueueData(1, protocol.Frame{Type: protocol.FrameData, StreamID: 1, Payload: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)

	deadline := time.After(time.Second)
	for writer.WriterQueueWaitP95() == 0 {
		select {
		case <-deadline:
			t.Fatal("writer queue wait was not sampled")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestFairFrameWriterReturnsSenderError(t *testing.T) {
	wantErr := errors.New("send failed")
	writer := NewFairFrameWriter(func(protocol.Frame) error { return wantErr }, FairWriterConfig{
		ControlQueueSize: 4, StreamQueueBytes: 16, QuantumBytes: 16,
	})
	defer writer.Close()
	if err := writer.EnqueueControl(protocol.Frame{Type: protocol.FramePong}); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- writer.Run(context.Background()) }()
	select {
	case err := <-errCh:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run error=%v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return sender error")
	}
}

func assertSentPayload(t *testing.T, sent <-chan string, want string) {
	t.Helper()
	select {
	case got := <-sent:
		if got != want {
			t.Fatalf("payload=%q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for payload %q", want)
	}
}

func TestFairFrameWriterRetiresEmptyStreamQueues(t *testing.T) {
	sent := make(chan protocol.Frame, 128)
	writer := NewFairFrameWriter(func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, FairWriterConfig{ControlQueueSize: 4, StreamQueueBytes: 1024, QuantumBytes: 32})
	defer writer.Close()

	for id := uint32(1); id <= 64; id++ {
		if err := writer.EnqueueData(id, protocol.Frame{Type: protocol.FrameData, StreamID: id, Payload: []byte("x")}); err != nil {
			t.Fatalf("enqueue stream %d: %v", id, err)
		}
	}
	if got := writer.StreamQueueCount(); got != 64 {
		t.Fatalf("queues before run=%d, want 64", got)
	}
	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { runDone <- writer.Run(ctx) }()

	for i := 0; i < 64; i++ {
		select {
		case <-sent:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d frames", i)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for writer.StreamQueueCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-runDone
	if got := writer.StreamQueueCount(); got != 0 {
		t.Fatalf("queues after drain=%d, want 0", got)
	}
	if len(writer.order) != 0 {
		t.Fatalf("round-robin list after drain=%d, want 0", len(writer.order))
	}
}

func TestFairFrameWriterPreservesOrderAcrossQueueRetirement(t *testing.T) {
	sent := make(chan string, 64)
	writer := NewFairFrameWriter(func(frame protocol.Frame) error {
		sent <- string(frame.Payload)
		return nil
	}, FairWriterConfig{ControlQueueSize: 4, StreamQueueBytes: 1024, QuantumBytes: 32})
	defer writer.Close()
	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { runDone <- writer.Run(ctx) }()
	defer func() {
		cancel()
		<-runDone
	}()

	if err := writer.EnqueueData(7, protocol.Frame{Type: protocol.FrameData, StreamID: 7, Payload: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	// Draining "a" makes stream 7 idle, which is exactly when its queue may be
	// retired. Reusing the ID afterwards must still deliver FIFO.
	assertSentPayload(t, sent, "a")
	for _, payload := range []string{"b", "c", "d"} {
		if err := writer.EnqueueData(7, protocol.Frame{Type: protocol.FrameData, StreamID: 7, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		assertSentPayload(t, sent, payload)
	}
}

func TestFairFrameWriterScanStaysBoundedAcrossManyStreams(t *testing.T) {
	writer := NewFairFrameWriter(func(protocol.Frame) error { return nil }, FairWriterConfig{
		ControlQueueSize: 4, StreamQueueBytes: 1024, QuantumBytes: 32,
	})
	defer writer.Close()
	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { runDone <- writer.Run(ctx) }()
	defer func() {
		cancel()
		<-runDone
	}()

	for id := uint32(1); id <= 5000; id++ {
		if err := writer.EnqueueData(id, protocol.Frame{Type: protocol.FrameData, StreamID: id, Payload: []byte("x")}); err != nil {
			t.Fatalf("enqueue stream %d: %v", id, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for writer.StreamQueueCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := writer.StreamQueueCount(); got > 8 {
		t.Fatalf("held queues after 5000 streams=%d, want a bounded number", got)
	}
	if got := len(writer.order); got > 8 {
		t.Fatalf("round-robin list after 5000 streams=%d, want a bounded length", got)
	}
}

func TestFairFrameWriterConcurrentEnqueueDuringRetirement(t *testing.T) {
	writer := NewFairFrameWriter(func(protocol.Frame) error { return nil }, FairWriterConfig{
		ControlQueueSize: 4, StreamQueueBytes: 1 << 20, QuantumBytes: 1024,
	})
	defer writer.Close()
	runDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { runDone <- writer.Run(ctx) }()
	defer func() {
		cancel()
		<-runDone
	}()

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for round := 0; round < 250; round++ {
				id := uint32(1 + (worker+round)%16)
				if err := writer.EnqueueData(id, protocol.Frame{Type: protocol.FrameData, StreamID: id, Payload: []byte("payload")}); err != nil {
					t.Errorf("worker %d round %d stream %d: %v", worker, round, id, err)
					return
				}
			}
		}(worker)
	}
	group.Wait()
}
