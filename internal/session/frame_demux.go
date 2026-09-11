package session

import (
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// FrameDemux separates the central receive loop from per-stream consumers.
// DATA frames enter an independent byte-bounded queue; all other frames are
// handed to the connection-level control handler without touching stream queues.
type FrameDemux struct {
	mu             sync.Mutex
	queues         map[uint32]*BoundedFrameQueue
	maxStreamBytes int
	control        func(protocol.Frame) error
	closed         bool
}

func NewFrameDemux(maxStreamBytes int, control func(protocol.Frame) error) *FrameDemux {
	if maxStreamBytes <= 0 {
		maxStreamBytes = 262144
	}
	return &FrameDemux{
		queues: make(map[uint32]*BoundedFrameQueue), maxStreamBytes: maxStreamBytes, control: control,
	}
}

func (d *FrameDemux) Dispatch(frame protocol.Frame) error {
	if d == nil {
		return ErrStreamQueueFull
	}
	if frame.Type != protocol.FrameData {
		if frame.Payload != nil {
			frame.Payload = append([]byte(nil), frame.Payload...)
		}
		if d.control == nil {
			return nil
		}
		return d.control(frame)
	}
	if frame.StreamID == 0 {
		return ErrStreamQueueFull
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrStreamQueueFull
	}
	queue, exists := d.queues[frame.StreamID]
	created := false
	if !exists {
		queue = NewBoundedFrameQueue(d.maxStreamBytes)
		d.queues[frame.StreamID] = queue
		created = true
	}
	d.mu.Unlock()

	if !queue.TryPush(frame) {
		if created {
			d.Remove(frame.StreamID)
		}
		return ErrStreamQueueFull
	}
	return nil
}

func (d *FrameDemux) Stream(streamID uint32) *BoundedFrameQueue {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	return d.queues[streamID]
}

func (d *FrameDemux) Remove(streamID uint32) {
	if d == nil {
		return
	}
	d.mu.Lock()
	queue := d.queues[streamID]
	delete(d.queues, streamID)
	d.mu.Unlock()
	if queue != nil {
		queue.Close()
	}
}

func (d *FrameDemux) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	queues := make([]*BoundedFrameQueue, 0, len(d.queues))
	for _, queue := range d.queues {
		queues = append(queues, queue)
	}
	d.queues = make(map[uint32]*BoundedFrameQueue)
	d.mu.Unlock()
	for _, queue := range queues {
		queue.Close()
	}
}
