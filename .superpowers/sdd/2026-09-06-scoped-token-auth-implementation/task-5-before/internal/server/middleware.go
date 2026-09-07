package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

type principalContextKey struct{}

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
