package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// clientScopedPrefix namespaces the endpoints a Client calls with its own service
// token. It is deliberately singular and separate from the admin-facing plural
// /api/v1/clients surface: the two authenticate with different credentials and
// must never be conflated, or a tunnel credential would gain a management view.
const clientScopedPrefix = "/api/v1/client/"

// ClientTokenValidator authenticates a client service token and returns the
// identity the client-scoped endpoints authorize against.
//
// The interface is intentionally narrow. These endpoints need "who owns this token
// and which Agents may it reach", never token lifecycle, so the API depends on the
// capability instead of on the whole auth.CredentialService. Depending on the
// concrete type would hand the management surface the ability to mint, rotate and
// revoke tokens it has no business touching.
type ClientTokenValidator interface {
	ValidateAs(ctx context.Context, raw string, expected storage.TokenType) (auth.TokenIdentity, error)
}

// SetClientTokenValidator installs the credential validator the runtime already
// uses for the client WebSocket handshake.
//
// Injection rather than construction is the point: auth.NewCredentialService starts
// a background "last used" worker that must be closed, and the runtime owns exactly
// one instance. Building a second one inside NewAPI would leak a goroutine per API
// and split last-used bookkeeping across two writers. Leaving the validator unset is
// a wiring bug, not an empty result, so the endpoints answer 503 rather than 401.
func (a *API) SetClientTokenValidator(validator ClientTokenValidator) {
	if a == nil {
		return
	}
	a.clientTokens = validator
}

// handleClientScoped claims the client-token namespace before the console bearer
// check runs.
//
// API.authenticate resolves api_tokens, the console login credential. A client
// service token lives in service_tokens, so routing it through the normal dispatch
// would reject the very credential the tunnel data plane accepts. The split mirrors
// handlePublicAuth: match the namespace first, then authenticate with the validator
// that namespace actually trusts.
func (a *API) handleClientScoped(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasPrefix(path, clientScopedPrefix) {
		return false
	}
	if a.clientTokens == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "client_token_validator_unavailable")
		return true
	}
	identity, err := a.clientTokens.ValidateAs(r.Context(), bearerToken(r), storage.TokenTypeClient)
	if err != nil {
		// The validator returns one opaque error for every rejection reason so a
		// revoked, expired, wrong-type and unknown token are indistinguishable from
		// outside. Echoing it would turn this endpoint into a token oracle.
		writeAPIError(w, http.StatusUnauthorized, "unauthenticated")
		return true
	}
	switch strings.Trim(strings.TrimPrefix(path, clientScopedPrefix), "/") {
	case "agents":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return true
		}
		a.listClientScopedAgents(w, r, identity)
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
	return true
}

// listClientScopedAgents renders the Agents one client token may point a tunnel at.
//
// This is the picker source for the tray's routing tab, which is why it answers with
// the connectivity flag: offering an Agent that holds no lease lets an operator
// configure a tunnel that can only ever fail at open time.
func (a *API) listClientScopedAgents(w http.ResponseWriter, r *http.Request, identity auth.TokenIdentity) {
	page, err := a.listAgentsForClientToken(r.Context(), identity, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	online, err := a.onlineAgentIDs(r.Context(), page.Items)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, agent := range page.Items {
		items = append(items, clientAgentView(agent, online[agent.ID]))
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

// clientAgentView is the whole response shape on purpose.
//
// A tray needs only enough to render a picker and to say whether the target is
// reachable. Reusing publicAgent would echo capabilities and ownership metadata that
// a client token has no use for, widening the blast radius of a leaked tunnel token
// for no functional gain.
func clientAgentView(agent storage.Agent, online bool) map[string]any {
	return map[string]any{"id": agent.ID, "name": agent.Name, "online": online}
}

// listAgentsForClientToken pages the Agents inside one client token's authority.
//
// Owner filtering happens in SQL. The enabled and scope filters run inside the
// accumulation loop rather than after paging: dropping rows from an already truncated
// page would silently shorten results and desynchronize the cursor, which is the
// failure mode AGENTS.md forbids for permission filtering. An empty scope means the
// token is unrestricted within its owner, matching how AuthorizeStream reads it.
func (a *API) listAgentsForClientToken(ctx context.Context, identity auth.TokenIdentity, cursor string, limit int) (storage.Page[storage.Agent], error) {
	if limit <= 0 {
		limit = 50
	}
	allowed := make(map[string]struct{}, len(identity.Scope.AgentIDs))
	for _, id := range identity.Scope.AgentIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	filter := storage.AgentListFilter{OwnerUserID: identity.OwnerUserID}
	result := storage.Page[storage.Agent]{}
	for {
		page, err := a.service.ListAgentsFiltered(ctx, filter, cursor, 500)
		if err != nil {
			return result, err
		}
		for index, agent := range page.Items {
			if !agent.Enabled {
				continue
			}
			if _, ok := allowed[agent.ID]; len(allowed) > 0 && !ok {
				continue
			}
			result.Items = append(result.Items, agent)
			if len(result.Items) == limit {
				// Stopping mid-page means more matches may follow either later in this
				// page or in a later one, so both have to keep HasMore true. The cursor
				// is the composite page key of the row just emitted, which is the only
				// form agentRepo.List can resume from.
				result.HasMore = page.HasMore || index < len(page.Items)-1
				result.NextCursor = storage.AgentCursor(agent)
				return result, nil
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}
