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

// CredentialStatus controls whether management list queries expose an active
// or logically deleted credential.
type CredentialStatus string

const (
	CredentialStatusActive  CredentialStatus = "active"
	CredentialStatusDeleted CredentialStatus = "deleted"
	CredentialStatusAll     CredentialStatus = "all"
)

// CredentialType is an allowlist for future credential formats. WebSSH only
// accepts SSH public keys in the current version.
type CredentialType string

const CredentialTypeSSHPublicKey CredentialType = "ssh_public_key"

// CredentialTypePassword stores an operator-provided password as an encrypted
// secret blob so WebSSH can authenticate without prompting the user. The
// plaintext never lives in this struct.
const CredentialTypePassword CredentialType = "password"

// Credential stores a public key only. Browser-side private-key extraction
// must never send private material to the Server.
type Credential struct {
	ID          string
	OwnerUserID string
	Name        string
	Type        CredentialType
	PublicKey   string
	Fingerprint string
	// SecretCiphertext, SecretNonce and SecretKeyID are base64-encoded
	// AES-GCM values wrapping one JSON payload (password and/or private key
	// plus passphrase). Empty for credentials without a stored secret.
	SecretCiphertext string
	SecretNonce      string
	SecretKeyID      string
	SecretVersion    int
	Enabled          bool
	DeletedAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Secret columns are base64-encoded AES-GCM material managed by
// auth.SecretStore, mirroring ServiceToken. A zero SecretCiphertext means the
// credential stores no secret and therefore cannot auto-authenticate.
func (c Credential) HasSecret() bool { return c.SecretCiphertext != "" }

type CredentialFilter struct {
	OwnerUserID string
	Type        CredentialType
	Status      CredentialStatus
	Keyword     string
}

type RemoteServerStatus string

const (
	RemoteServerStatusEnabled  RemoteServerStatus = "enabled"
	RemoteServerStatusDisabled RemoteServerStatus = "disabled"
	RemoteServerStatusDeleted  RemoteServerStatus = "deleted"
	RemoteServerStatusAll      RemoteServerStatus = "all"
)

// RemoteServer is the durable management record used to create a WebSSH
// session. It contains no SSH password or private key.
type RemoteServer struct {
	ID              string
	OwnerUserID     string
	Name            string
	Host            string
	Port            int
	DefaultUsername string
	CredentialID    string
	AgentID         string
	Enabled         bool
	DeletedAt       *time.Time
	LastConnectedAt *time.Time
	LastResult      string
	LastErrorClass  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type RemoteServerFilter struct {
	OwnerUserID string
	AgentID     string
	Status      RemoteServerStatus
	Keyword     string
}

type WebSSHSessionStatus string

const (
	WebSSHSessionPending WebSSHSessionStatus = "pending"
	WebSSHSessionActive  WebSSHSessionStatus = "active"
	WebSSHSessionClosed  WebSSHSessionStatus = "closed"
	WebSSHSessionExpired WebSSHSessionStatus = "expired"
)

// WebSSHSession owns the one-time browser WebSocket ticket. TicketHash is a
// SHA-256 digest and must never contain the raw ticket value.
type WebSSHSession struct {
	ID              string
	OwnerUserID     string
	RemoteServerID  string
	AgentID         string
	OwnerNodeID     string
	TicketHash      string
	TicketExpiresAt time.Time
	Status          WebSSHSessionStatus
	CreatedAt       time.Time
	ExpiresAt       time.Time
	ConnectedAt     *time.Time
	ClosedAt        *time.Time
	CloseReason     string
}

type AgentPolicy struct {
	ID           string
	AgentID      string
	TargetHost   string
	TargetPort   int
	Protocol     string
	AllowedCIDRs string
	AllowedPorts string
	DeletedAt    *time.Time
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

type ClientInstanceFilter struct {
	OwnerUserID  string
	TokenID      string
	ServerNodeID string
	Status       string
	AgentID      string
	Keyword      string
}

type ClientConnectionFilter struct {
	ClientInstanceID string
	OwnerUserID      string
	TokenID          string
	ServerNodeID     string
	IncludeExpired   bool
}

type ClientInstance struct {
	ID           string
	OwnerUserID  string
	InstanceID   string
	Metadata     string
	Capabilities string
	ReportedAt   time.Time
	LastSeenAt   time.Time
	ExpiresAt    *time.Time
	Stale        bool
	UpdatedAt    time.Time
}

type ClientConnectionLease struct {
	ConnectionID     string
	ClientInstanceID string
	TokenID          string
	OwnerUserID      string
	ServerNodeID     string
	ConnectionEpoch  int64
	ActiveStreams    int64
	HealthScore      int64
	AcquiredAt       time.Time
	ExpiresAt        time.Time
	UpdatedAt        time.Time
}

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
