// Package proxyentry implements the policy kernel of the tp-* managed HTTP
// proxy entry. OpenResty only moves bytes and injects trusted headers; every
// authorization decision (route identity, source ACL, Basic auth, target
// validation, capacity) happens here so it can be unit tested and so there is
// exactly one authoritative implementation.
package proxyentry

import "net/http"

// Error pairs an HTTP status with the stable error code returned to clients.
//
// The code is part of the public contract: user documentation and the admin
// troubleshooting table key off it, so codes must never be renamed without a
// deprecation window. The message is deliberately generic for the 403 family
// (unknown route, disabled route and ACL denial all render the same text) so
// that an outsider cannot enumerate which tp-* names exist; only logs and
// metrics distinguish the reason.
type Error struct {
	Status  int
	Code    string
	message string
	headers map[string]string
}

// NewError builds an *Error. headers is variadic so the common case stays
// readable while the 407/503 paths can attach mandatory response headers.
func NewError(status int, code, message string, headers ...map[string]string) *Error {
	e := &Error{Status: status, Code: code, message: message}
	if len(headers) > 0 {
		e.headers = headers[0]
	}
	return e
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.message == "" {
		return e.Code
	}
	return e.message
}

// HTTPHeaders returns the headers that must accompany this error's response.
// It returns nil (not an empty map) when there are none, so callers can use a
// simple nil check and the zero value never invents headers.
func (e *Error) HTTPHeaders() map[string]string {
	if e == nil || len(e.headers) == 0 {
		return nil
	}
	return e.headers
}

// Stable error codes for the proxy entry. Keep in sync with the error table in
// docs/user-guide/http-proxy-entry.md and with docs/api/openapi.yaml.
var (
	ErrRouteIdentityInvalid = NewError(http.StatusForbidden, "proxy_route_identity_invalid", "proxy route identity is invalid")
	ErrRouteUnavailable     = NewError(http.StatusForbidden, "proxy_route_unavailable", "proxy route is unavailable")
	ErrSourceDenied         = NewError(http.StatusForbidden, "proxy_source_denied", "source address is not allowed by this proxy route")
	ErrAuthRequired         = NewError(http.StatusProxyAuthRequired, "proxy_auth_required", "proxy authentication required", proxyAuthenticateHeader())
	ErrAuthFailed           = NewError(http.StatusProxyAuthRequired, "proxy_auth_failed", "proxy authentication failed", proxyAuthenticateHeader())
	ErrAuthBackoff          = NewError(http.StatusProxyAuthRequired, "proxy_auth_backoff", "too many failed proxy authentication attempts", proxyAuthenticateHeader())
	ErrTargetDenied         = NewError(http.StatusForbidden, "proxy_target_denied", "target address is denied by policy")
	ErrTargetInvalid        = NewError(http.StatusBadRequest, "proxy_target_invalid", "target host or port is invalid")
	ErrEgressUnavailable    = NewError(http.StatusBadGateway, "proxy_egress_unavailable", "egress agent is unavailable")
	ErrEgressTimeout        = NewError(http.StatusGatewayTimeout, "proxy_egress_timeout", "egress agent did not answer in time")
	ErrCapacityExhausted    = NewError(http.StatusServiceUnavailable, "proxy_capacity_exhausted", "proxy tunnel capacity exhausted", map[string]string{"Retry-After": "5"})
	ErrSecretUnavailable    = NewError(http.StatusServiceUnavailable, "credential_secret_unavailable", "credential secret storage is unavailable")
)

// proxyAuthenticateHeader keeps the 407 challenge byte-identical on every
// failure path. Without it browsers and curl do not prompt for credentials and
// instead surface the 407 as a bare error, so it is attached to all three
// auth-related sentinels rather than only to ErrAuthRequired.
func proxyAuthenticateHeader() map[string]string {
	return map[string]string{"Proxy-Authenticate": `Basic realm="TunnelMesh", charset="UTF-8"`}
}
