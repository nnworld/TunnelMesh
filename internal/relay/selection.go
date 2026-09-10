package relay

import (
	"context"
	"io"
)

// AgentConnectionTarget is the transport-neutral result of connection
// selection. Local targets are opened directly; remote targets carry the
// Server node identity used by RelayService.
type AgentConnectionTarget struct {
	AgentID         string
	ConnectionID    string
	ConnectionEpoch int64
	Local           bool
	ServerNodeID    string
	ServerNodeEpoch int64
	ActiveStreams   int64
	HealthScore     int64
}

type ConnectionSelector interface {
	Select(ctx context.Context, agentID, protocol string) (AgentConnectionTarget, error)
}

// SelectedTransport keeps route handlers independent of connection placement.
// The selector owns locality, health, and least-connections policy; this
// adapter only translates the target into the local or remote transport call.
type SelectedTransport struct {
	selector ConnectionSelector
	local    NodeTransport
	remote   *RelayService
}

func NewSelectedTransport(selector ConnectionSelector, local NodeTransport, remote *RelayService) *SelectedTransport {
	return &SelectedTransport{selector: selector, local: local, remote: remote}
}

func (t *SelectedTransport) OpenStream(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, error) {
	if t == nil || t.selector == nil {
		return nil, ErrNodeDisconnected
	}
	target, err := t.selector.Select(ctx, request.AgentID, request.Protocol)
	if err != nil {
		return nil, err
	}
	if target.AgentID != "" {
		request.AgentID = target.AgentID
	}
	request.TargetConnectionID = target.ConnectionID
	request.TargetConnectionEpoch = target.ConnectionEpoch
	if target.Local {
		if t.local == nil {
			return nil, ErrNodeDisconnected
		}
		request.NodeID = ""
		request.Epoch = 0
		return t.local.OpenStream(ctx, request)
	}
	if t.remote == nil || target.ServerNodeID == "" {
		return nil, ErrNodeDisconnected
	}
	request.NodeID = target.ServerNodeID
	request.Epoch = target.ServerNodeEpoch
	return t.remote.OpenStream(ctx, request)
}

func (t *SelectedTransport) Close() error {
	if t == nil || t.local == nil {
		return nil
	}
	return t.local.Close()
}
