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
	// AuthSource records where the primary credential comes from. MFARequired
	// is a per-account override that wins over the global policy.
	AuthSource  AuthSource
	MFARequired bool
}

// AuthSource identifies the credential origin of an account.
type AuthSource string

const (
	// AuthSourceLocal is a username/password account created by an administrator
	// or by the bootstrap flow.
	AuthSourceLocal AuthSource = "local"
	// AuthSourceOIDC is a just-in-time provisioned account that has no local
	// password. Its PasswordHash is PasswordHashNone.
	AuthSourceOIDC AuthSource = "oidc"
	// AuthSourceMixed is a local account that also has at least one linked
	// external identity, so both login paths work.
	AuthSourceMixed AuthSource = "mixed"
)

// PasswordHashNone is stored for accounts that must never authenticate with a
// local password. It is not a valid Argon2id PHC string, so verification always
// fails without needing a nullable column that SQLite cannot add in place.
const PasswordHashNone = "*"

// MFAMode is the global second-factor enforcement policy.
type MFAMode string

const (
	MFAModeDisabled MFAMode = "disabled"
	MFAModeOptional MFAMode = "optional"
	MFAModeRequired MFAMode = "required"
)

// MFAStatus distinguishes an unconfirmed enrollment from an active one. A
// pending enrollment never satisfies an MFA requirement.
type MFAStatus string

const (
	MFAStatusPending MFAStatus = "pending"
	MFAStatusEnabled MFAStatus = "enabled"
)

// ChallengeKind identifies the purpose of a short-lived auth_challenges row.
type ChallengeKind string

const (
	ChallengeKindLoginMFA    ChallengeKind = "login_mfa"
	ChallengeKindOIDCState   ChallengeKind = "oidc_state"
	ChallengeKindLoginTicket ChallengeKind = "login_ticket"
)

// AuthSettings is the runtime authentication policy. It is a singleton row that
// an administrator owns; process configuration only seeds the first value.
type AuthSettings struct {
	MFAMode                  MFAMode
	DeviceTrustEnabled       bool
	DeviceTrustTTLSeconds    int64
	AllowTrustedDeviceBypass bool
	MaxTrustedDevices        int
	SessionTokenTTLSeconds   int64
	UpdatedAt                time.Time
}

// OIDCProvider is one configured identity provider. ClientSecretCiphertext and
// ClientSecretNonce are base64-encoded AES-GCM values produced by the secret
// store; the plaintext secret is never persisted or returned.
type OIDCProvider struct {
	ID                     string
	Name                   string
	DisplayName            string
	Issuer                 string
	ClientID               string
	ClientSecretCiphertext string
	ClientSecretNonce      string
	ClientSecretKeyID      string
	ClientSecretVersion    int
	Scopes                 string
	RedirectURI            string
	AuthorizationEndpoint  string
	TokenEndpoint          string
	UserinfoEndpoint       string
	JWKSURI                string
	IDTokenAlgs            string
	UsernameClaim          string
	RoleMappings           string
	DefaultRole            string
	AuthoritativeRoles     bool
	AutoCreateUsers        bool
	FetchUserinfo          bool
	PublicListed           bool
	Enabled                bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// HasSecret reports whether a client secret is stored for this provider.
func (p OIDCProvider) HasSecret() bool { return p.ClientSecretCiphertext != "" }

// UserIdentity links one external subject to one local account.
type UserIdentity struct {
	ID          string
	UserID      string
	ProviderID  string
	Subject     string
	Email       string
	DisplayName string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// UserMFA holds the encrypted TOTP enrollment for one account.
type UserMFA struct {
	UserID           string
	SecretCiphertext string
	SecretNonce      string
	SecretKeyID      string
	SecretVersion    int
	Status           MFAStatus
	EnrolledAt       time.Time
	EnabledAt        *time.Time
	LastUsedAt       *time.Time
	// LastUsedStep is the replay guard: a TOTP counter value may only be
	// accepted once, so a code replayed inside the skew window is rejected.
	LastUsedStep int64
	UpdatedAt    time.Time
}

// UserRecoveryCode stores one single-use bypass code as a one-way hash.
type UserRecoveryCode struct {
	ID        string
	UserID    string
	CodeHash  string
	UsedAt    *time.Time
	CreatedAt time.Time
}

// UserDevice is a trusted browser or API client. Only the token hash is stored.
type UserDevice struct {
	ID         string
	UserID     string
	TokenHash  string
	Name       string
	UserAgent  string
	IP         string
	TrustedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt *time.Time
	RevokedAt  *time.Time
}

// AuthChallenge is short-lived encrypted state shared by every cluster node so
// a login, OIDC redirect, or ticket exchange can finish on any node.
type AuthChallenge struct {
	ID                string
	Kind              ChallengeKind
	UserID            string
	PayloadCiphertext string
	PayloadNonce      string
	PayloadKeyID      string
	PayloadVersion    int
	Attempts          int
	MaxAttempts       int
	ConsumedAt        *time.Time
	ExpiresAt         time.Time
	CreatedAt         time.Time
}

// AuthLoginAttempt is one brute-force counter bucket. The key is a hash of the
// username and client IP, so neither value is recoverable from the table.
type AuthLoginAttempt struct {
	BucketKey    string
	Attempts     int
	WindowStart  time.Time
	BlockedUntil *time.Time
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

// CredentialTypeProxyBasic stores an HTTP proxy username/password pair used by
// the managed `tp-*` forward-proxy entry. The username lives in PublicKey so
// list views can render it without decrypting anything; the password only
// exists inside the encrypted secret blob.
const CredentialTypeProxyBasic CredentialType = "proxy_basic"

// ProtocolHTTPProxy marks a managed route that terminates a forward-proxy
// request instead of reverse-proxying a fixed target. The row lives in the same
// tunnels table so route administration, audit and ownership stay unified, but
// it is resolved by a separate snapshot: the reverse-proxy resolver must never
// see these rows, or a tp-* hostname would match a path-based route.
const ProtocolHTTPProxy = "http-proxy"

// ProxyTargetWildcard is the sentinel stored in tunnels.target_host for
// http-proxy routes, whose real target comes from each request. target_port
// stores the companion sentinel 0. Both columns are NOT NULL, so a route that
// has no fixed target still needs a representable value.
const ProxyTargetWildcard = "*"

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

// ClientInstanceFilter narrows the Client observability list. Status is
// presence only; metadata freshness is a separate axis so a connected Client is
// never labelled by an expired or missing metadata snapshot.
type ClientInstanceFilter struct {
	OwnerUserID   string
	TokenID       string
	ServerNodeID  string
	Status        string
	MetadataState string
	AgentID       string
	Keyword       string
}

// ClientInstanceSummary aggregates the whole filtered Client population, not a
// single cursor page, so the console counters cannot disagree with the table.
type ClientInstanceSummary struct {
	Total               int64
	Online              int64
	ActiveConnections   int64
	ActiveStreams       int64
	MetadataUnavailable int64
	MetadataStale       int64
}

func (s ClientInstanceSummary) IsZero() bool { return s == ClientInstanceSummary{} }

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
