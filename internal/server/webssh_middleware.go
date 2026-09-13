package server

import (
	"errors"
	"net/http"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

var ErrWebSSHOriginNotAllowed = errors.New("webssh origin is not allowed")

func validateWebSSHOrigin(security config.SecurityConfig, r *http.Request) error {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return ErrWebSSHOriginNotAllowed
	}
	_, normalized, ok := normalizeOrigin(values[0])
	if !ok {
		return ErrWebSSHOriginNotAllowed
	}
	if len(security.AllowedOrigins) > 0 && !containsNormalizedOrigin(normalized, security.AllowedOrigins) {
		return ErrWebSSHOriginNotAllowed
	}
	return nil
}
