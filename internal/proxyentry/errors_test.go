package proxyentry_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestErrorsCarryStableCodesAndStatus(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{proxyentry.ErrRouteIdentityInvalid, http.StatusForbidden, "proxy_route_identity_invalid"},
		{proxyentry.ErrRouteUnavailable, http.StatusForbidden, "proxy_route_unavailable"},
		{proxyentry.ErrSourceDenied, http.StatusForbidden, "proxy_source_denied"},
		{proxyentry.ErrAuthRequired, http.StatusProxyAuthRequired, "proxy_auth_required"},
		{proxyentry.ErrAuthFailed, http.StatusProxyAuthRequired, "proxy_auth_failed"},
		{proxyentry.ErrAuthBackoff, http.StatusProxyAuthRequired, "proxy_auth_backoff"},
		{proxyentry.ErrTargetDenied, http.StatusForbidden, "proxy_target_denied"},
		{proxyentry.ErrTargetInvalid, http.StatusBadRequest, "proxy_target_invalid"},
		{proxyentry.ErrEgressUnavailable, http.StatusBadGateway, "proxy_egress_unavailable"},
		{proxyentry.ErrEgressTimeout, http.StatusGatewayTimeout, "proxy_egress_timeout"},
		{proxyentry.ErrCapacityExhausted, http.StatusServiceUnavailable, "proxy_capacity_exhausted"},
		{proxyentry.ErrSecretUnavailable, http.StatusServiceUnavailable, "credential_secret_unavailable"},
	}
	for _, tc := range cases {
		var pe *proxyentry.Error
		if !errors.As(tc.err, &pe) {
			t.Fatalf("%v is not a proxyentry.Error", tc.err)
		}
		if pe.Status != tc.status || pe.Code != tc.code {
			t.Fatalf("got %d/%s want %d/%s", pe.Status, pe.Code, tc.status, tc.code)
		}
	}
	if got := (&proxyentry.Error{}).HTTPHeaders(); got != nil {
		// zero value must not invent headers
		t.Fatalf("empty error produced headers %v", got)
	}
	var authErr *proxyentry.Error
	errors.As(proxyentry.ErrAuthFailed, &authErr)
	if authErr.HTTPHeaders()["Proxy-Authenticate"] == "" {
		t.Fatal("407 must advertise Proxy-Authenticate")
	}
	var capErr *proxyentry.Error
	errors.As(proxyentry.ErrCapacityExhausted, &capErr)
	if capErr.HTTPHeaders()["Retry-After"] != "5" {
		t.Fatal("503 must carry Retry-After: 5")
	}
}
