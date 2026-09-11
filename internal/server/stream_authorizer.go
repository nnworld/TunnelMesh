package server

import (
	"context"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type ClientRegistration struct {
	Token string
}

// ClientSessionPrincipal retains only non-secret identity derived during the
// HTTP upgrade. Raw bearer tokens never enter session state.
type ClientSessionPrincipal struct {
	ConnectionID string
	Identity     auth.TokenIdentity
	StrictOpen   bool
}

type StreamAuthorizer interface {
	Authorize(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error
}

type CredentialStreamAuthorizer struct {
	credentials *auth.CredentialService
}

func NewCredentialStreamAuthorizer(credentials *auth.CredentialService) *CredentialStreamAuthorizer {
	return &CredentialStreamAuthorizer{credentials: credentials}
}

func (a *CredentialStreamAuthorizer) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
	if a == nil || a.credentials == nil || principal.ConnectionID == "" {
		return auth.ErrForbidden
	}
	return a.credentials.AuthorizeStream(ctx, principal.Identity, auth.StreamAuthorizationRequest{
		AgentID:    request.AgentID,
		Protocol:   request.Protocol,
		TargetHost: request.TargetHost,
		TargetPort: request.TargetPort,
	})
}
