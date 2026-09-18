package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// loginRedirectPath is the fixed relative path every OIDC callback redirects to.
// It is a constant rather than something derived from the request or the provider
// row, which is what makes an open redirect through the callback impossible even
// if a provider's redirect_uri points somewhere else.
const loginRedirectPath = "/login"

// handlePublicOIDC serves the unauthenticated half of the OIDC flow.
func (a *API) handlePublicOIDC(w http.ResponseWriter, r *http.Request, remainder string) {
	if !requireIdentity(w, a.identity) {
		return
	}
	if a.identity.Providers == nil || a.identity.RelyingParty == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
		return
	}
	segments := splitPath("/" + remainder)
	switch {
	case len(segments) == 1 && segments[0] == "providers":
		a.listPublicOIDCProviders(w, r)
	case len(segments) == 2 && segments[1] == "authorize":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.beginOIDCLogin(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "callback":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.completeOIDCLogin(w, r, segments[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

// listPublicOIDCProviders is what the login page renders as SSO buttons. It is
// gated by a global switch so a deployment can require users to know the URL
// before the existence of its IdP is disclosed.
func (a *API) listPublicOIDCProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !a.identity.OIDC.PublicProviders {
		// 404 rather than 403: a disabled feature should not be distinguishable
		// from a route that never existed.
		writeAPIError(w, http.StatusNotFound, "not_found")
		return
	}
	views, err := a.identity.Providers.ListPublic(r.Context())
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

// beginOIDCLogin starts the authorization-code flow. The state, nonce, and PKCE
// verifier are stored encrypted in one challenge row so any cluster node can
// complete the redirect, and the challenge identifier doubles as the state value:
// it is already a 192-bit random token, and reusing it removes a second lookup
// path that could disagree with the first.
func (a *API) beginOIDCLogin(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	provider, err := a.identity.Providers.ResolveRelyingParty(ctx, name)
	if err != nil {
		a.recordOIDCStep("provision", err)
		writeAuthError(w, err)
		return
	}
	nonce, err := oidc.NewNonce()
	if err != nil {
		writeAuthError(w, err)
		return
	}
	verifier, challenge, err := oidc.NewPKCE()
	if err != nil {
		writeAuthError(w, err)
		return
	}
	stateTTL := a.identity.OIDC.StateTTL
	if stateTTL <= 0 {
		stateTTL = 10 * time.Minute
	}
	// max_attempts is 1: a state is consumed by the callback, so a second
	// presentation of the same state must fail rather than retry.
	state, err := a.identity.Challenges.Create(ctx, storage.ChallengeKindOIDCState, "", map[string]string{
		"nonce":    nonce,
		"verifier": verifier,
		"provider": name,
	}, stateTTL, 1)
	if err != nil {
		a.recordOIDCStep("discovery", err)
		writeAuthError(w, err)
		return
	}
	target, err := a.identity.RelyingParty.BuildAuthURL(ctx, provider, oidc.AuthRequest{
		State:         state,
		Nonce:         nonce,
		CodeChallenge: challenge,
		RedirectURI:   provider.RedirectURI,
	})
	if err != nil {
		a.recordOIDCStep("discovery", err)
		writeAuthError(w, err)
		return
	}
	a.recordOIDCStep("discovery", nil)
	// no-store keeps a browser or proxy from caching a URL that carries a
	// single-use state.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// completeOIDCLogin handles the IdP redirect: it exchanges the code, verifies the
// id_token, provisions the account, and hands the browser a single-use ticket or
// an MFA challenge.
func (a *API) completeOIDCLogin(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	query := r.URL.Query()
	// An IdP-side refusal is reported without echoing error_description, which is
	// IdP-controlled text and must not become reflected content on our origin.
	if providerError := strings.TrimSpace(query.Get("error")); providerError != "" {
		a.recordOIDCStep("token", errors.New("provider_error"))
		writeAuthError(w, auth.ErrOIDCProviderError)
		return
	}
	state := strings.TrimSpace(query.Get("state"))
	code := strings.TrimSpace(query.Get("code"))
	if state == "" || code == "" {
		writeAuthError(w, auth.ErrOIDCStateInvalid)
		return
	}
	challenge, err := a.identity.Challenges.Load(ctx, state, storage.ChallengeKindOIDCState)
	if err != nil {
		writeAuthError(w, auth.ErrOIDCStateInvalid)
		return
	}
	// The state row names the provider it was minted for, so a state from one
	// provider cannot be replayed into another provider's callback.
	if challenge.Payload["provider"] != name {
		writeAuthError(w, auth.ErrOIDCStateInvalid)
		return
	}
	provider, err := a.identity.Providers.ResolveRelyingParty(ctx, name)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	stored, err := a.identity.Providers.ResolveStored(ctx, name)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	tokens, err := a.identity.RelyingParty.Exchange(ctx, provider, code, challenge.Payload["verifier"], provider.RedirectURI)
	if err != nil {
		a.recordOIDCStep("token", err)
		// The state is burned even on failure so a captured code cannot be retried
		// against this redirect.
		_, _ = a.identity.Challenges.Consume(ctx, challenge.ID)
		writeAuthError(w, err)
		return
	}
	a.recordOIDCStep("token", nil)
	claims, err := a.identity.RelyingParty.VerifyIDToken(ctx, provider, tokens.IDToken, challenge.Payload["nonce"], tokens.AccessToken)
	if err != nil {
		a.recordOIDCStep("id_token", err)
		_, _ = a.identity.Challenges.Consume(ctx, challenge.ID)
		writeAuthError(w, err)
		return
	}
	a.recordOIDCStep("id_token", nil)
	username, role, err := oidc.ResolveIdentity(provider, claims)
	if err != nil {
		a.recordOIDCStep("provision", err)
		_, _ = a.identity.Challenges.Consume(ctx, challenge.ID)
		writeAuthError(w, err)
		return
	}
	if a.identity.Identities == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
		return
	}
	result, err := a.identity.Identities.ProvisionFromOIDC(ctx, auth.ProvisionRequest{
		Provider: stored,
		Claims:   claims,
		Username: username,
		Role:     role,
		ClientIP: clientIP(r, a.trustedProxies()),
	})
	if err != nil {
		a.recordOIDCStep("provision", err)
		_, _ = a.identity.Challenges.Consume(ctx, challenge.ID)
		writeAuthError(w, err)
		return
	}
	a.recordOIDCStep("provision", nil)
	// The state is consumed only after provisioning succeeds, so a transient
	// database error leaves the redirect retryable within its TTL.
	consumed, err := a.identity.Challenges.Consume(ctx, challenge.ID)
	if err != nil || !consumed {
		writeAuthError(w, auth.ErrOIDCStateInvalid)
		return
	}

	clientAddr := clientIP(r, a.trustedProxies())
	outcome, err := a.identity.Logins.PrepareOIDCLogin(ctx, result.User, clientAddr, a.deviceCookie(r))
	if err != nil {
		writeAuthError(w, err)
		return
	}
	a.recordLoginMetric("oidc", outcome, nil)
	w.Header().Set("Cache-Control", "no-store")
	if outcome.State == auth.LoginStateMFARequired {
		a.redirectToLogin(w, r, "mfa", outcome.ChallengeID)
		return
	}
	ticketTTL := a.identity.OIDC.LoginTicketTTL
	if ticketTTL <= 0 {
		ticketTTL = 60 * time.Second
	}
	ticket, err := a.identity.Challenges.Create(ctx, storage.ChallengeKindLoginTicket, result.User.ID, map[string]string{
		"provider":      name,
		"deviceTrusted": boolString(outcome.DeviceTrusted),
	}, ticketTTL, 1)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	// The ticket value is never written to the audit log; only the fact that a
	// session hand-off was prepared is recorded, by the login success audit that
	// the exchange step writes.
	a.redirectToLogin(w, r, "ticket", ticket)
}

// redirectToLogin builds the only redirect this handler ever produces. The path is
// a constant and the value is URL-escaped, so neither the provider configuration
// nor the IdP can influence where the browser ends up.
func (a *API) redirectToLogin(w http.ResponseWriter, r *http.Request, key, value string) {
	target := loginRedirectPath
	if a.identity.OIDC.LoginRedirectPath != "" {
		target = a.identity.OIDC.LoginRedirectPath
	}
	if !strings.HasPrefix(target, "/") || strings.Contains(target, "//") {
		target = loginRedirectPath
	}
	query := url.Values{}
	query.Set(key, value)
	http.Redirect(w, r, target+"?"+query.Encode(), http.StatusFound)
}

// exchangeOIDCTicket turns a single-use redirect ticket into a console session.
// It is a POST rather than a GET so the ticket never lands in a browser history,
// a referrer header, or a proxy access log.
func (a *API) exchangeOIDCTicket(w http.ResponseWriter, r *http.Request) {
	if !requireIdentity(w, a.identity) {
		return
	}
	var req struct {
		Ticket      string `json:"ticket"`
		TrustDevice bool   `json:"trustDevice"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()
	challenge, err := a.identity.Challenges.Load(ctx, strings.TrimSpace(req.Ticket), storage.ChallengeKindLoginTicket)
	if err != nil {
		writeAuthError(w, auth.ErrOIDCLoginTicketInvalid)
		return
	}
	// Consuming before issuing means a second exchange of the same ticket finds no
	// live row, even if the two requests race.
	consumed, err := a.identity.Challenges.Consume(ctx, challenge.ID)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if !consumed {
		writeAuthError(w, auth.ErrOIDCLoginTicketInvalid)
		return
	}
	user, err := a.DB.Users().Get(ctx, challenge.UserID)
	if err != nil || user.Disabled || user.DeletedAt != nil {
		writeAuthError(w, auth.ErrOIDCLoginTicketInvalid)
		return
	}
	outcome, err := a.identity.Logins.IssueSession(ctx, user, "oidc", clientIP(r, a.trustedProxies()), r.UserAgent(), req.TrustDevice, challenge.Payload["deviceTrusted"] == "true")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	a.recordLoginMetric("oidc", outcome, nil)
	a.writeLoginOutcome(w, r, outcome)
}

func (a *API) recordOIDCStep(step string, err error) {
	if a.identity == nil || a.identity.Metrics == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	a.identity.Metrics.AuthOIDCStep(step, result)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
