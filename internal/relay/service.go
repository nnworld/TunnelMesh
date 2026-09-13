package relay

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var (
	ErrEpoch            = errors.New("relay: stale epoch")
	ErrNodeDisconnected = errors.New("relay: node disconnected")
	ErrBackpressure     = errors.New("relay: backpressure")
)

type StreamRequest struct {
	NodeID  string
	AgentID string
	// CaseInsensitiveAgentID is set only for dynamic public Host routes. DNS
	// labels are case-insensitive while Agent IDs are case-sensitive.
	CaseInsensitiveAgentID bool
	Epoch                  int64
	// TargetConnectionID and TargetConnectionEpoch pin a stream to one pooled
	// physical Agent connection. Empty values select from the logical pool.
	TargetConnectionID    string
	TargetConnectionEpoch int64
	StreamID              uint32
	StrictOpen            bool
	InitialWindow         uint32
	Protocol              string
	TargetHost            string
	TargetPort            int
	TargetScheme          string
	HostHeader            string
	TLSServerName         string
	Metadata              []byte
}

// RelayOpenMetadata carries the relay-neutral opening contract. StrictOpen is
// duplicated from Request so adapters can expose negotiated semantics without
// making callers inspect transport-specific fields.
type RelayOpenMetadata struct {
	Request     StreamRequest
	StrictOpen  bool
	OpenTimeout time.Duration
}

// RelayOpenResult is the terminal result of a strict stream open. The payload
// is deliberately bounded by protocol.MaxOpenResultPayload.
type RelayOpenResult struct {
	Payload protocol.OpenResultPayload
}

// CloseAgentConnectionRequest is the inter-server control message for closing
// one physical Agent connection. ConnectionEpoch is the registry lease epoch;
// the owning Server resolves it to the session's protocol epoch locally.
type CloseAgentConnectionRequest struct {
	AgentID           string
	ConnectionID      string
	ConnectionEpoch   int64
	RequestedByNodeID string
}

type CloseAgentConnectionFunc func(context.Context, CloseAgentConnectionRequest) error

// CloseClientConnectionRequest is the inter-server control message for one
// physical Client WebSocket lease. RequestedByNodeID is verified against the
// authenticated Server-node identity by the receiving runtime.
type CloseClientConnectionRequest struct {
	ConnectionID      string
	ConnectionEpoch   int64
	RequestedByNodeID string
}

// CloseWebSSHConnectionRequest is the inter-server control message for one
// browser WebSSH session owned by a Server process.
type CloseWebSSHConnectionRequest struct {
	SessionID         string
	RequestedByNodeID string
}

type CloseClientConnectionFunc func(context.Context, CloseClientConnectionRequest) error
type CloseWebSSHConnectionFunc func(context.Context, CloseWebSSHConnectionRequest) error
type NodeTransport interface {
	OpenStream(context.Context, StreamRequest) (io.ReadWriteCloser, error)
	Close() error
}

// OpenResultTransport is an optional capability for transports that can
// distinguish terminal open failures from a successfully created byte stream.
type OpenResultTransport interface {
	NodeTransport
	OpenStreamResult(context.Context, StreamRequest) (io.ReadWriteCloser, RelayOpenResult, error)
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

func (r *RelayService) OpenStreamResult(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, RelayOpenResult, error) {
	tr, err := r.transport(req)
	if err != nil {
		return nil, RelayOpenResult{Payload: openResultFailure(protocol.OpenResultStageRelay, protocol.OpenResultCodeAgentOffline)}, err
	}
	resultTransport, ok := tr.(OpenResultTransport)
	if !ok {
		if req.StrictOpen {
			return nil, RelayOpenResult{Payload: openResultFailure(protocol.OpenResultStageRelay, protocol.OpenResultCodeUnsupportedCapability)}, nil
		}
		conn, openErr := tr.OpenStream(ctx, req)
		if openErr != nil || conn == nil {
			return nil, RelayOpenResult{Payload: openResultFailure(protocol.OpenResultStageRelay, protocol.OpenResultCodeInternalError)}, openErr
		}
		return conn, RelayOpenResult{Payload: protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}}, nil
	}
	return resultTransport.OpenStreamResult(ctx, req)
}

func (r *RelayService) transport(req StreamRequest) (NodeTransport, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if req.NodeID == "" {
		if r.local == nil {
			return nil, ErrNodeDisconnected
		}
		return r.local, nil
	}
	entry, ok := r.nodes[req.NodeID]
	if !ok {
		return nil, ErrNodeDisconnected
	}
	if req.Epoch != entry.epoch {
		return nil, ErrEpoch
	}
	return entry.transport, nil
}

func openResultFailure(stage protocol.OpenResultStage, code protocol.OpenResultCode) protocol.OpenResultPayload {
	return protocol.OpenResultPayload{Accepted: false, Stage: stage, Code: code, Retryable: code == protocol.OpenResultCodeQueueFull || code == protocol.OpenResultCodeTimeout}
}
