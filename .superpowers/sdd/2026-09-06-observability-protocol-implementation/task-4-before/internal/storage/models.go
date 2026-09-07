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
	Scope       string
	Type        TokenType
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	Address    string
	Epoch      int64
	Metadata   string
	LastSeenAt *time.Time
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type AgentRuntimeMetadata struct {
	AgentID    string
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

type AgentLease struct {
	AgentID    string
	NodeID     string
	Epoch      int64
	TTL        time.Duration
	AcquiredAt time.Time
	ExpiresAt  time.Time
	UpdatedAt  time.Time
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

// CursorPage is an alias retained for callers that use the longer name.
type CursorPage[T any] = Page[T]
