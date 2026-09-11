package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type principalContextKey struct{}
type agentAuthenticationContextKey struct{}
type clientPrincipalContextKey struct{}

type agentConnectionAuthentication struct {
	Identity  auth.TokenIdentity
	Principal auth.Principal
	Legacy    bool
}

func principalFromContext(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(auth.Principal)
	return p, ok && p.UserID != ""
}

func withPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// bearerToken extracts only the conventional Authorization form. Query and
// cookie tokens are intentionally unsupported to avoid accidental leakage.
func bearerToken(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(v) < 7 || !strings.EqualFold(v[:6], "Bearer") || (len(v) > 6 && v[6] != ' ') {
		return ""
	}
	return strings.TrimSpace(v[7:])
}

func agentAuthenticationFromContext(ctx context.Context) (agentConnectionAuthentication, bool) {
	authentication, ok := ctx.Value(agentAuthenticationContextKey{}).(agentConnectionAuthentication)
	return authentication, ok
}

func clientPrincipalFromContext(ctx context.Context) (ClientSessionPrincipal, bool) {
	principal, ok := ctx.Value(clientPrincipalContextKey{}).(ClientSessionPrincipal)
	return principal, ok && principal.ConnectionID != "" && principal.Identity.TokenID != ""
}

func clientWebSocketHandshake(security config.SecurityConfig, authenticate func(context.Context, string) (ClientSessionPrincipal, error)) func(*websocket.Config, *http.Request) error {
	return func(wsConfig *websocket.Config, r *http.Request) error {
		if len(security.AllowedHosts) > 0 && !exactHostAllowed(r.Host, security.AllowedHosts) {
			return fmt.Errorf("websocket host is not allowed")
		}
		values := r.Header.Values("Origin")
		if len(values) != 1 {
			return fmt.Errorf("websocket origin is not allowed")
		}
		origin, normalized, ok := normalizeOrigin(values[0])
		if !ok || (len(security.AllowedOrigins) > 0 && !containsNormalizedOrigin(normalized, security.AllowedOrigins)) {
			return fmt.Errorf("websocket origin is not allowed")
		}
		raw := bearerToken(r)
		if raw == "" || authenticate == nil {
			return auth.ErrUnauthenticated
		}
		principal, err := authenticate(r.Context(), raw)
		if err != nil {
			return auth.ErrUnauthenticated
		}
		authenticatedRequest := r.WithContext(context.WithValue(r.Context(), clientPrincipalContextKey{}, principal))
		authenticatedRequest.Header = r.Header.Clone()
		authenticatedRequest.Header.Del("Authorization")
		*r = *authenticatedRequest
		wsConfig.Origin = origin
		selected, _ := protocol.SelectSubprotocol(r.Header.Values("Sec-WebSocket-Protocol"))
		if selected == "" {
			wsConfig.Protocol = nil
		} else {
			wsConfig.Protocol = []string{selected}
		}
		principal.StrictOpen = selected == protocol.SubprotocolOpenResult ||
			selected == protocol.SubprotocolFlowControl ||
			selected == protocol.SubprotocolClientMetadata
		principal.MetadataEnabled = selected == protocol.SubprotocolClientMetadata
		*r = *r.WithContext(context.WithValue(r.Context(), clientPrincipalContextKey{}, principal))
		return nil
	}
}

func agentWebSocketHandshake(security config.SecurityConfig, authenticate func(context.Context, string) (agentConnectionAuthentication, error)) func(*websocket.Config, *http.Request) error {
	return func(wsConfig *websocket.Config, r *http.Request) error {
		if len(security.AllowedHosts) > 0 && !exactHostAllowed(r.Host, security.AllowedHosts) {
			return fmt.Errorf("websocket host is not allowed")
		}
		values := r.Header.Values("Origin")
		if len(values) != 1 {
			return fmt.Errorf("websocket origin is not allowed")
		}
		origin, normalized, ok := normalizeOrigin(values[0])
		if !ok || (len(security.AllowedOrigins) > 0 && !containsNormalizedOrigin(normalized, security.AllowedOrigins)) {
			return fmt.Errorf("websocket origin is not allowed")
		}
		raw := bearerToken(r)
		if raw == "" || authenticate == nil {
			return auth.ErrUnauthenticated
		}
		authentication, err := authenticate(r.Context(), raw)
		if err != nil {
			return auth.ErrUnauthenticated
		}
		authenticatedRequest := r.WithContext(context.WithValue(r.Context(), agentAuthenticationContextKey{}, authentication))
		authenticatedRequest.Header = r.Header.Clone()
		authenticatedRequest.Header.Del("Authorization")
		*r = *authenticatedRequest
		wsConfig.Origin = origin
		return nil
	}
}

func exactHostAllowed(raw string, allowed []string) bool {
	want, ok := normalizeHost(raw)
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if normalized, valid := normalizeHost(candidate); valid && normalized == want {
			return true
		}
	}
	return false
}

func normalizeHost(raw string) (string, bool) {
	return config.NormalizeAllowedHost(raw)
}

func containsNormalizedOrigin(want string, allowed []string) bool {
	for _, candidate := range allowed {
		_, normalized, ok := normalizeOrigin(candidate)
		if ok && normalized == want {
			return true
		}
	}
	return false
}

func normalizeOrigin(raw string) (*url.URL, string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u, u.String(), true
}
