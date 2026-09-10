package registry

import (
	"context"
	"errors"
	"time"
)

var (
	ErrLeaseHeld    = errors.New("agent lease is held by another node")
	ErrFencing      = errors.New("stale registry epoch")
	ErrNotFound     = errors.New("registry owner not found")
	ErrLeaseExpired = errors.New("registry lease expired")
	ErrRevoked      = errors.New("registry owner revoked")
)

type NodeRegistration struct {
	NodeID       string
	Address      string
	Metadata     string
	AgentID      string
	InstanceID   string
	ConnectionID string
	ServerNodeID string
	TTL          time.Duration
}

// Node is retained as a concise registration alias for callers that model a
// server node separately from its agent lease.
type Node = NodeRegistration

// Lease is the portable ownership view shared by database and etcd adapters.
// NodeOwner contains the same fields plus node address and metadata.
type Lease struct {
	AgentID   string
	NodeID    string
	Epoch     int64
	ExpiresAt time.Time
}

type NodeOwner struct {
	NodeID          string
	Address         string
	Metadata        string
	AgentID         string
	InstanceID      string
	ConnectionID    string
	ServerNodeID    string
	Epoch           int64
	ConnectionEpoch int64
	ServerNodeEpoch int64
	ActiveStreams   int64
	HealthScore     int64
	ExpiresAt       time.Time
	LeaseID         int64
}

type EventType string

const (
	EventRegistered EventType = "registered"
	EventUpdated    EventType = "updated"
	EventRevoked    EventType = "revoked"
)

type RegistryEvent struct {
	Type  EventType
	Owner NodeOwner
}

type NodeRegistry interface {
	Register(context.Context, NodeRegistration) (NodeOwner, error)
	KeepAlive(context.Context, NodeOwner, time.Duration) (NodeOwner, error)
	UpdateConnectionStats(context.Context, NodeOwner) error
	ResolveAgent(context.Context, string) (NodeOwner, error)
	ListAgentConnections(context.Context, string) ([]NodeOwner, error)
	Watch(context.Context, string) (<-chan RegistryEvent, error)
	Revoke(context.Context, NodeOwner) error
	Close() error
}
