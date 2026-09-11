package session

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var (
	ErrControlQueueFull = errors.New("session: control frame queue is full")
	ErrStreamQueueFull  = errors.New("session: stream frame queue is full")
)

type FairWriterConfig struct {
	ControlQueueSize int
	StreamQueueBytes int
	QuantumBytes     int
}

type fairStreamQueue struct {
	queue *BoundedFrameQueue
}

type controlItem struct {
	frame protocol.Frame
	done  chan struct{}
}

type durationSamples struct {
	mu     sync.Mutex
	values []time.Duration
}

func (s *durationSamples) Record(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append(s.values, duration)
	if len(s.values) > 64 {
		s.values = s.values[len(s.values)-64:]
	}
}

func (s *durationSamples) P95() time.Duration {
	s.mu.Lock()
	values := append([]time.Duration(nil), s.values...)
	s.mu.Unlock()
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (len(values)*95 + 99) / 100
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

// FairFrameWriter serializes all sends on one connection while isolating
// per-stream backpressure. Control frames have priority; DATA streams use a
// bounded quantum per round so a bulk transfer cannot starve small streams.
type FairFrameWriter struct {
	mu          sync.Mutex
	cond        *sync.Cond
	control     []controlItem
	streams     map[uint32]*fairStreamQueue
	order       []uint32
	cursor      int
	sender      func(protocol.Frame) error
	config      FairWriterConfig
	waitSamples durationSamples
	closed      bool
	closeOnce   sync.Once
}

func NewFairFrameWriter(sender func(protocol.Frame) error, config FairWriterConfig) *FairFrameWriter {
	if sender == nil {
		sender = func(protocol.Frame) error { return nil }
	}
	if config.ControlQueueSize <= 0 {
		config.ControlQueueSize = 64
	}
	if config.StreamQueueBytes <= 0 {
		config.StreamQueueBytes = 262144
	}
	if config.QuantumBytes <= 0 {
		config.QuantumBytes = 32768
	}
	writer := &FairFrameWriter{
		control: make([]controlItem, 0, config.ControlQueueSize),
		streams: make(map[uint32]*fairStreamQueue), sender: sender, config: config,
	}
	writer.cond = sync.NewCond(&writer.mu)
	return writer
}

func (w *FairFrameWriter) EnqueueControl(frame protocol.Frame) error {
	// HALF_CLOSE is stream-scoped and must not overtake DATA that is already
	// queued for the same stream. Other controls remain latency-sensitive and
	// keep connection-level priority.
	if frame.Type == protocol.FrameHalfClose && frame.StreamID != 0 {
		return w.EnqueueData(frame.StreamID, frame)
	}
	return w.enqueueControl(frame, nil)
}

// EnqueueControlSync enqueues a control frame and returns only after that
// exact frame has been sent. It is used for metadata reporting while PING and
// other latency-sensitive controls remain asynchronous.
func (w *FairFrameWriter) EnqueueControlSync(frame protocol.Frame) error {
	done := make(chan struct{})
	if err := w.enqueueControl(frame, done); err != nil {
		return err
	}
	<-done
	return nil
}

func (w *FairFrameWriter) enqueueControl(frame protocol.Frame, done chan struct{}) error {
	if w == nil {
		return ErrControlQueueFull
	}
	if frame.Payload != nil {
		frame.Payload = append([]byte(nil), frame.Payload...)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrControlQueueFull
	}
	if len(w.control) >= w.config.ControlQueueSize {
		return ErrControlQueueFull
	}
	w.control = append(w.control, controlItem{frame: frame, done: done})
	w.cond.Signal()
	return nil
}

func (w *FairFrameWriter) EnqueueData(streamID uint32, frame protocol.Frame) error {
	if w == nil || streamID == 0 {
		return ErrStreamQueueFull
	}
	frame.StreamID = streamID
	w.mu.Lock()
	stream, exists := w.streams[streamID]
	created := false
	if !exists {
		stream = &fairStreamQueue{queue: NewBoundedFrameQueue(w.config.StreamQueueBytes)}
		w.streams[streamID] = stream
		w.order = append(w.order, streamID)
		// A newly active stream must receive its next turn immediately;
		// otherwise an existing bulk stream can consume two consecutive turns.
		w.cursor = len(w.order) - 1
		created = true
	}
	w.mu.Unlock()

	if !stream.queue.TryPush(frame) {
		if created {
			w.removeStream(streamID, stream)
		}
		return ErrStreamQueueFull
	}
	w.cond.Signal()
	return nil
}

func (w *FairFrameWriter) removeStream(streamID uint32, stream *fairStreamQueue) {
	w.mu.Lock()
	if current, ok := w.streams[streamID]; ok && current == stream {
		delete(w.streams, streamID)
		for i, id := range w.order {
			if id == streamID {
				w.order = append(w.order[:i], w.order[i+1:]...)
				if w.cursor > i {
					w.cursor--
				}
				break
			}
		}
	}
	w.mu.Unlock()
	stream.queue.Close()
}

func (w *FairFrameWriter) Run(ctx context.Context) error {
	if w == nil {
		return nil
	}
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			w.cond.Broadcast()
		case <-watchDone:
		}
	}()
	defer close(watchDone)

	for {
		item, ok := w.next(ctx)
		if !ok {
			if err := ctx.Err(); err != nil {
				_ = w.Close()
				return err
			}
			return nil
		}
		if err := w.sender(item.frame); err != nil {
			if item.done != nil {
				close(item.done)
			}
			_ = w.Close()
			return err
		}
		if item.done != nil {
			close(item.done)
		}
		w.finishSend()
		if ctx.Err() != nil {
			_ = w.Close()
			return ctx.Err()
		}
	}
}

func (w *FairFrameWriter) finishSend() {
	w.mu.Lock()
	w.cond.Broadcast()
	w.mu.Unlock()
}

// Drain waits until every queued frame and the current in-flight send have
// completed. It lets a connection emit GOAWAY only after earlier stream work.
func (w *FairFrameWriter) Drain() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for !w.closed && (len(w.control) > 0 || w.hasDataLocked()) {
		w.cond.Wait()
	}
}

func (w *FairFrameWriter) next(ctx context.Context) (controlItem, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if w.closed {
			return controlItem{}, false
		}
		if ctx != nil && ctx.Err() != nil {
			return controlItem{}, false
		}
		if len(w.control) > 0 {
			item := w.control[0]
			w.control[0] = controlItem{}
			w.control = w.control[1:]
			return item, true
		}
		if frame, ok := w.nextDataLocked(); ok {
			return controlItem{frame: frame}, true
		}
		w.cond.Wait()
	}
}

func (w *FairFrameWriter) nextDataLocked() (protocol.Frame, bool) {
	for range w.order {
		index := w.cursor % len(w.order)
		streamID := w.order[index]
		stream := w.streams[streamID]
		w.cursor = (index + 1) % len(w.order)
		if stream == nil {
			continue
		}
		if frame, ok := stream.queue.TryPop(); ok {
			w.waitSamples.Record(stream.queue.LastWait())
			return frame, true
		}
	}
	return protocol.Frame{}, false
}

func (w *FairFrameWriter) WriterQueueWaitP95() time.Duration {
	if w == nil {
		return 0
	}
	return w.waitSamples.P95()
}

func (w *FairFrameWriter) hasDataLocked() bool {
	for _, stream := range w.streams {
		if stream.queue.Len() > 0 {
			return true
		}
	}
	return false
}

func (w *FairFrameWriter) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		pending := append([]controlItem(nil), w.control...)
		w.control = nil
		streams := make([]*fairStreamQueue, 0, len(w.streams))
		for _, stream := range w.streams {
			streams = append(streams, stream)
		}
		w.streams = make(map[uint32]*fairStreamQueue)
		w.order = nil
		w.mu.Unlock()
		for _, item := range pending {
			if item.done != nil {
				close(item.done)
			}
		}
		for _, stream := range streams {
			stream.queue.Close()
		}
		w.cond.Broadcast()
	})
	return nil
}
