package storage

import "time"

// User is an authenticated TunnelMesh account. PasswordHash is always a
// one-way hash; repositories never receive or return a clear-text password.
type User struct {
	ID           string
	Username     string
	Role         string
	PasswordHash string
	Disabled     bool
	DeletedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type APIToken struct {
	ID             string
	UserID         string
	TokenHash      string
	IdempotencyKey string
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
	CreatedAt      time.Time
}

// Token is retained as a concise alias for callers that use the domain term.
type Token = APIToken

// TokenType identifies the service principal that may present a credential.
type TokenType string

const (
	TokenTypeAgent      TokenType = "agent"
	TokenTypeClient     TokenType = "client"
	TokenTypeServerNode TokenType = "server_node"
)

// ServiceToken stores a one-way service credential and its authorization
// scope. Lifecycle state is derived from the timestamp fields.
type ServiceToken struct {
	ID          string
	OwnerUserID string
	AgentID     string
	NodeID      string
	Prefix      string
	TokenHash   string
	// SecretCiphertext and SecretNonce are base64-encoded AES-GCM values. They
	// are intentionally absent for legacy hash-only tokens.
	SecretCiphertext string
	SecretNonce      string
	SecretKeyID      string
	SecretVersion    int
	SecretLastReadAt *time.Time
	Scope            string
	Type             TokenType
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
	LastUsedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Agent struct {
	ID           string
	Name         string
	OwnerUserID  string
	Capabilities string
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AgentPolicy struct {
	ID           string
	AgentID      string
	TargetHost   string
	TargetPort   int
	Protocol     string
	AllowedCIDRs string
	AllowedPorts string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type TunnelGroup struct {
	ID        string
	UserID    string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Tunnel struct {
	ID         string
	GroupID    string
	AgentID    string
	Protocol   string
	Domain     string
	PathPrefix string
	TargetHost string
	TargetPort int
	PublicPort int
	Status     string
	Config     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ServerNode struct {
	ID         string
	Name       string
	Address    string
	Epoch      int64
	Metadata   string
	Enabled    bool
	DeletedAt  *time.Time
	LastSeenAt *time.Time
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ServerNodeStats aggregates active Agent connections owned by one Server
// node. HealthScore is the minimum connection score so the UI exposes the
// least healthy active path rather than hiding degradation behind an average.
type ServerNodeStats struct {
	NodeID            string
	ActiveConnections int64
	ActiveStreams     int64
	HealthScore       int64
}

type AgentRuntimeMetadata struct {
	AgentID    string
	InstanceID string
	NodeID     string
	Epoch      int64
	Revision   int64
	Metadata   string
	ReportedAt time.Time
	LastSeenAt time.Time
	ExpiresAt  *time.Time
	Stale      bool
	UpdatedAt  time.Time
}

// AgentRuntimeStats is a bounded, one-minute aggregate. It deliberately
// contains counters and quantiles only; payloads, targets and identifiers
// with unbounded cardinality never enter the history table.
type AgentRuntimeStats struct {
	ID               string
	AgentID          string
	NodeID           string
	Epoch            int64
	WindowStart      time.Time
	WindowEnd        time.Time
	Connections      int64
	ActiveStreams    int64
	BytesIn          int64
	BytesOut         int64
	HeartbeatTotal   int64
	HeartbeatSuccess int64
	HeartbeatRTTP50  time.Duration
	HeartbeatRTTP95  time.Duration
	Reconnects       int64
	StreamErrors     int64
	CreatedAt        time.Time
}

// AgentProbeResult is the persisted probe summary. Response bodies and
// arbitrary error text are intentionally not represented by this model.
type AgentProbeResult struct {
	ProbeID    string
	AgentID    string
	NodeID     string
	Epoch      int64
	Kind       string
	Result     string
	ErrorClass string
	Duration   time.Duration
	ObservedAt time.Time
}

type AgentLease struct {
	AgentID         string
	NodeID          string
	InstanceID      string
	ConnectionID    string
	ServerNodeID    string
	Epoch           int64
	ConnectionEpoch int64
	ActiveStreams   int64
	HealthScore     int64
	TTL             time.Duration
	AcquiredAt      time.Time
	ExpiresAt       time.Time
	UpdatedAt       time.Time
}

// Lease is a compatibility alias used by registry adapters.
type Lease = AgentLease

type AuditLog struct {
	ID           string
	ActorUserID  string
	Action       string
	ResourceType string
	ResourceID   string
	Details      string
	CreatedAt    time.Time
}

// AuditFilter describes exact management-facing audit search criteria. Time is
// inclusive so administrators can query a complete day or event window.
type AuditFilter struct {
	ActorUserID  string
	Action       string
	ResourceType string
	ResourceID   string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
}

type IdempotencyRecord struct {
	Key        string
	UserID     string
	Response   string
	StatusCode int
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

type Page[T any] struct {
	Items      []T
	NextCursor string
	HasMore    bool
}

// CursorPage retains the longer compatibility name without using a generic
// type alias, which is experimental in Go 1.23 and crashes go/types tooling.
type CursorPage[T any] Page[T]
