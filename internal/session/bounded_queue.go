package session

import (
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// BoundedFrameQueue is a byte-bounded FIFO. TryPush never blocks, which keeps
// the central receive loop independent of a slow per-stream consumer.
type BoundedFrameQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	frames   []protocol.Frame
	bytes    int
	maxBytes int
	closed   bool
	waits    []time.Time
	lastWait time.Duration
}

func NewBoundedFrameQueue(maxBytes int) *BoundedFrameQueue {
	if maxBytes <= 0 {
		maxBytes = 1
	}
	queue := &BoundedFrameQueue{maxBytes: maxBytes}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *BoundedFrameQueue) TryPush(frame protocol.Frame) bool {
	if q == nil {
		return false
	}
	// The queue takes ownership at this boundary. Copying once here prevents
	// the receive buffer from being reused while a slow stream holds the frame.
	if frame.Payload != nil {
		frame.Payload = append([]byte(nil), frame.Payload...)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	size := len(frame.Payload)
	if q.bytes+size > q.maxBytes || len(q.frames) >= q.maxBytes {
		return false
	}
	q.frames = append(q.frames, frame)
	q.bytes += size
	q.waits = append(q.waits, time.Now())
	q.cond.Signal()
	return true
}

func (q *BoundedFrameQueue) TryPop() (protocol.Frame, bool) {
	if q == nil {
		return protocol.Frame{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.popLocked()
}

func (q *BoundedFrameQueue) Pop() (protocol.Frame, bool) {
	if q == nil {
		return protocol.Frame{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if frame, ok := q.popLocked(); ok {
			return frame, true
		}
		if q.closed {
			return protocol.Frame{}, false
		}
		q.cond.Wait()
	}
}

func (q *BoundedFrameQueue) popLocked() (protocol.Frame, bool) {
	if len(q.frames) == 0 {
		return protocol.Frame{}, false
	}
	frame := q.frames[0]
	q.frames[0] = protocol.Frame{}
	q.frames = q.frames[1:]
	q.bytes -= len(frame.Payload)
	if len(q.waits) > 0 {
		q.lastWait = time.Since(q.waits[0])
		q.waits = q.waits[1:]
	}
	return frame, true
}

// Capacity reports the byte bound this queue was created with. Operators use it
// to confirm a configured buffer size actually reached the data plane.
func (q *BoundedFrameQueue) Capacity() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.maxBytes
}

func (q *BoundedFrameQueue) LastWait() time.Duration {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastWait
}

func (q *BoundedFrameQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.frames)
}

func (q *BoundedFrameQueue) Bytes() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.bytes
}

// CloseAfterDrain stops accepting frames and wakes blocked consumers, but keeps
// the bytes already queued deliverable. A retiring stream that must still see the
// request it was sent uses this; Close discards them.
func (q *BoundedFrameQueue) CloseAfterDrain() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *BoundedFrameQueue) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.closed = true
	q.frames = nil
	q.bytes = 0
	q.mu.Unlock()
	q.cond.Broadcast()
}
