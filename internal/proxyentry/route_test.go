package proxyentry_test

import (
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestRouteActiveAndRequiresAuth(t *testing.T) {
	active := proxyentry.Route{Status: proxyentry.StatusActive, AuthMode: proxyentry.AuthModeBasic}
	if !active.Active() || !active.RequiresAuth() {
		t.Fatal("active basic route must require auth")
	}
	if (proxyentry.Route{Status: proxyentry.StatusDisabled}).Active() {
		t.Fatal("disabled route must not be active")
	}
	if (proxyentry.Route{Status: proxyentry.StatusActive, AuthMode: proxyentry.AuthModeNone}).RequiresAuth() {
		t.Fatal("none mode must not require auth")
	}
}
