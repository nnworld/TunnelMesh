package relay

import (
	"context"
	"errors"
	"io"
	"sync"
)

var (
	ErrEpoch            = errors.New("relay: stale epoch")
	ErrNodeDisconnected = errors.New("relay: node disconnected")
	ErrBackpressure     = errors.New("relay: backpressure")
)

type StreamRequest struct {
	NodeID     string
	AgentID    string
	Epoch      int64
	StreamID   uint32
	Protocol   string
	TargetHost string
	TargetPort int
	Metadata   []byte
}
type NodeTransport interface {
	OpenStream(context.Context, StreamRequest) (io.ReadWriteCloser, error)
	Close() error
}
type nodeEntry struct {
	epoch     int64
	transport NodeTransport
}
type RelayService struct {
	mu    sync.RWMutex
	nodes map[string]nodeEntry
	local NodeTransport
}

func NewRelayService(local ...NodeTransport) *RelayService {
	r := &RelayService{nodes: make(map[string]nodeEntry)}
	if len(local) > 0 {
		r.local = local[0]
	}
	return r
}
func (r *RelayService) RegisterNode(id string, epoch int64, tr NodeTransport) {
	if id == "" || tr == nil {
		return
	}
	r.mu.Lock()
	if current, ok := r.nodes[id]; ok && current.epoch >= epoch {
		r.mu.Unlock()
		return
	}
	r.nodes[id] = nodeEntry{epoch: epoch, transport: tr}
	r.mu.Unlock()
}
func (r *RelayService) UnregisterNode(id string) {
	r.mu.Lock()
	e, ok := r.nodes[id]
	delete(r.nodes, id)
	r.mu.Unlock()
	if ok {
		_ = e.transport.Close()
	}
}
func (r *RelayService) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	r.mu.RLock()
	if req.NodeID == "" {
		tr := r.local
		r.mu.RUnlock()
		if tr == nil {
			return nil, ErrNodeDisconnected
		}
		return tr.OpenStream(ctx, req)
	}
	e, ok := r.nodes[req.NodeID]
	if !ok {
		r.mu.RUnlock()
		return nil, ErrNodeDisconnected
	}
	if req.Epoch != e.epoch {
		r.mu.RUnlock()
		return nil, ErrEpoch
	}
	// Keep the read lock while opening so UnregisterNode cannot close the
	// transport between lookup and use.
	conn, err := e.transport.OpenStream(ctx, req)
	r.mu.RUnlock()
	return conn, err
}
