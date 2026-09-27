package vpn

import "net/http"

// Error pairs an HTTP status with the stable error code returned to clients.
//
// The shape mirrors proxyentry.Error on purpose: one type carries both halves
// of the contract so a handler cannot answer 409 with a code that means 400.
//
// Code is part of the public contract. docs/api/openapi.yaml, the admin console
// and the user troubleshooting tables key off these strings, so a code must
// never be renamed without a deprecation window. Message is the human-readable
// detail; it is written to the response but never to metrics, where only the
// low-cardinality Code is safe as a label value.
type Error struct {
	Status  int
	Code    string
	message string
}

// NewError builds an *Error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, message: message}
}

// Error renders the message, falling back to the code so a caller can never put
// an empty msg into the { code, msg, data } envelope. A nil *Error renders as
// the empty string rather than panicking, which keeps error-path logging safe.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.message == "" {
		return e.Code
	}
	return e.message
}

// WithMessage returns a copy carrying a more specific message while keeping the
// stable code and status. Sentinels are shared across requests, so a caller that
// wants request-specific detail must not mutate them.
func (e *Error) WithMessage(message string) *Error {
	if e == nil {
		return nil
	}
	return &Error{Status: e.Status, Code: e.Code, message: message}
}

// Stable management-API error codes. Keep in sync with the table in the
// embedded VPN gateway design spec §12.1 and with docs/api/openapi.yaml.
var (
	// ErrPeerInvalid reports a field validation failure: name, CIDR, port or
	// quota out of range.
	ErrPeerInvalid = NewError(http.StatusBadRequest, "vpn_peer_invalid", "vpn peer is invalid")
	// ErrIPPoolInvalid reports that server.vpn.ip_pool or node_subnet_size is
	// unusable. It surfaces as a request error because the operator only
	// discovers it when the first peer is issued.
	ErrIPPoolInvalid = NewError(http.StatusBadRequest, "vpn_ip_pool_invalid", "vpn ip pool configuration is invalid")
	// ErrPeerNotFound means the peer does not exist or is not visible to the
	// calling principal. Both cases render identically so an outsider cannot
	// enumerate peer IDs owned by someone else; 403 would leak existence.
	ErrPeerNotFound = NewError(http.StatusNotFound, "vpn_peer_not_found", "vpn peer not found")
	// ErrPeerConflict reports a name or public key collision, and also the
	// attempt to mutate a peer that is already revoked, since revocation is a
	// terminal state.
	ErrPeerConflict = NewError(http.StatusConflict, "vpn_peer_conflict", "vpn peer conflicts with an existing peer")
	// ErrAgentCapabilityMissing means the peer asked for ICMP but the egress
	// agent has not negotiated stream_icmp_echo.v1. Returning success instead
	// would hand the user a peer that looks pingable and is not.
	ErrAgentCapabilityMissing = NewError(http.StatusConflict, "vpn_agent_capability_missing", "egress agent lacks the required vpn capability")
	// ErrIPPoolExhausted means the node's subnet has no free /32 left.
	ErrIPPoolExhausted = NewError(http.StatusConflict, "vpn_ip_pool_exhausted", "vpn ip pool is exhausted")
	// ErrNodeDisabled means the target node has server.vpn.enabled set to
	// false, so it owns no VPN resources at all.
	ErrNodeDisabled = NewError(http.StatusConflict, "vpn_node_disabled", "vpn is not enabled on this node")
	// ErrSecretUnavailable means the AES-GCM key that seals peer private keys
	// is not configured. It reuses the existing credential code rather than
	// inventing a second name for one root cause, and it never degrades to
	// storing or returning the key in plaintext.
	ErrSecretUnavailable = NewError(http.StatusServiceUnavailable, "credential_secret_unavailable", "credential secret storage is unavailable")
	// ErrCapacityExhausted means the configured max_peers quota is reached.
	// It is 503 rather than 409 because the caller did nothing wrong and a
	// retry after capacity is raised would succeed.
	ErrCapacityExhausted = NewError(http.StatusServiceUnavailable, "vpn_capacity_exhausted", "vpn peer capacity exhausted")
	// ErrNotImplemented marks an endpoint that is specified but whose data
	// plane dependency has not shipped yet. It answers 501 instead of a 200
	// with an empty list so a client cannot read "not built" as "nothing
	// happening".
	ErrNotImplemented = NewError(http.StatusNotImplemented, "vpn_not_implemented", "vpn capability is not implemented in this release")
)
