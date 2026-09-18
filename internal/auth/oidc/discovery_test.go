package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryParsesEndpoints(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	endpoints, err := idp.rp.Discover(context.Background(), idp.provider)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if endpoints.Authorization != idp.server.URL+"/authorize" || endpoints.Token != idp.server.URL+"/token" || endpoints.JWKS != idp.server.URL+"/jwks.json" || endpoints.Userinfo != idp.server.URL+"/userinfo" {
		t.Fatalf("endpoints = %+v", endpoints)
	}
	if endpoints.Issuer != idp.server.URL {
		t.Fatalf("issuer = %q", endpoints.Issuer)
	}
}

func TestDiscoveryMissingJWKSURIFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.mu.Lock()
	idp.omitJWKSURI = true
	idp.mu.Unlock()
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v, want ErrDiscoveryFailed", err)
	}
}

// TestDiscoveryJWKSOverrideSatisfiesRequirement proves an operator can point at a
// JWKS URI directly when the discovery document does not publish one.
func TestDiscoveryJWKSOverrideSatisfiesRequirement(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.mu.Lock()
	idp.omitJWKSURI = true
	idp.mu.Unlock()
	provider := idp.provider
	provider.JWKSURI = idp.server.URL + "/jwks.json"
	endpoints, err := idp.rp.Discover(context.Background(), provider)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if endpoints.JWKS != provider.JWKSURI {
		t.Fatalf("jwks = %q", endpoints.JWKS)
	}
}

func TestDiscoveryOverridesWin(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	provider := idp.provider
	provider.AuthorizationEndpoint = idp.server.URL + "/custom-authorize"
	provider.TokenEndpoint = idp.server.URL + "/custom-token"
	endpoints, err := idp.rp.Discover(context.Background(), provider)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if endpoints.Authorization != provider.AuthorizationEndpoint || endpoints.Token != provider.TokenEndpoint {
		t.Fatalf("overrides were not honoured: %+v", endpoints)
	}
}

func TestDiscoveryNonHTTPSIssuerFails(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	rp := NewRelyingParty(plain.Client(), DefaultConfig(), nil)
	_, err := rp.Discover(context.Background(), Provider{Issuer: plain.URL, ClientID: "c"})
	if !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v, want ErrDiscoveryFailed", err)
	}
	for _, issuer := range []string{"", "example.com", "ftp://example.com", "://bad", "/relative"} {
		if _, err := rp.Discover(context.Background(), Provider{Issuer: issuer, ClientID: "c"}); !errors.Is(err, ErrDiscoveryFailed) {
			t.Fatalf("issuer %q err = %v", issuer, err)
		}
	}
}

func TestDiscoveryNon200Fails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v, want ErrDiscoveryFailed", err)
	}
}

func TestDiscoveryInvalidJSONFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	})
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v", err)
	}
}

// TestDiscoveryOversizedBodyFails proves the read is bounded, so a hostile IdP
// cannot exhaust server memory through its metadata document.
func TestDiscoveryOversizedBodyFails(t *testing.T) {
	idp := newTestIDP(t, Config{MaxBodyBytes: 64})
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	})
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v, want ErrDiscoveryFailed", err)
	}
}

func TestDiscoveryTimeoutFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
	})
	client := idp.server.Client()
	client.Timeout = 20 * time.Millisecond
	rp := NewRelyingParty(client, DefaultConfig(), nil)
	if _, err := rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v, want ErrDiscoveryFailed", err)
	}
}

// TestDiscoveryIssuerMismatchFails covers the IdP mix-up defence: a metadata
// document that names a different issuer is not the provider we configured.
func TestDiscoveryIssuerMismatchFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	other := idp.server.URL + "/elsewhere"
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 other,
			"authorization_endpoint": idp.server.URL + "/authorize",
			"token_endpoint":         idp.server.URL + "/token",
			"jwks_uri":               idp.server.URL + "/jwks.json",
		})
	})
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v, want ErrDiscoveryFailed", err)
	}
}

// TestDiscoveryMissingRequiredEndpointFails covers an IdP that publishes a valid
// document without the endpoints a code flow needs.
func TestDiscoveryMissingRequiredEndpointFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": idp.server.URL, "jwks_uri": idp.server.URL + "/jwks.json"})
	})
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoveryRelativeEndpointFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 idp.server.URL,
			"authorization_endpoint": "/authorize",
			"token_endpoint":         idp.server.URL + "/token",
			"jwks_uri":               idp.server.URL + "/jwks.json",
		})
	})
	if _, err := idp.rp.Discover(context.Background(), idp.provider); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoveryIsCachedThenInvalidatable(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	var discoveryRequests int64
	handler := idp.server.Config.Handler
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, wellKnownPath) {
			discoveryRequests++
		}
		handler.ServeHTTP(w, r)
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := idp.rp.Discover(ctx, idp.provider); err != nil {
			t.Fatal(err)
		}
	}
	if discoveryRequests != 1 {
		t.Fatalf("discovery fetched %d times, want 1", discoveryRequests)
	}
	idp.rp.InvalidateMetadata(idp.provider.Issuer)
	if _, err := idp.rp.Discover(ctx, idp.provider); err != nil {
		t.Fatal(err)
	}
	if discoveryRequests != 2 {
		t.Fatalf("invalidate did not force a refetch: %d", discoveryRequests)
	}
}

func TestDiscoveryCacheExpires(t *testing.T) {
	idp := newTestIDP(t, Config{JWKSCacheTTL: 10 * time.Millisecond})
	var discoveryRequests int64
	handler := idp.server.Config.Handler
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, wellKnownPath) {
			discoveryRequests++
		}
		handler.ServeHTTP(w, r)
	})
	ctx := context.Background()
	if _, err := idp.rp.Discover(ctx, idp.provider); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := idp.rp.Discover(ctx, idp.provider); err != nil {
		t.Fatal(err)
	}
	if discoveryRequests != 2 {
		t.Fatalf("cache did not expire: %d fetches", discoveryRequests)
	}
}

// TestDiscoveryCompleteOverridesSkipNetwork proves a fully overridden provider
// needs no IdP round trip at all, which is what makes an air-gapped override
// configuration usable.
func TestDiscoveryCompleteOverridesSkipNetwork(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	idp.server.Close()
	provider := idp.provider
	provider.AuthorizationEndpoint = "https://idp.example.com/authorize"
	provider.TokenEndpoint = "https://idp.example.com/token"
	provider.JWKSURI = "https://idp.example.com/jwks.json"
	endpoints, err := idp.rp.Discover(context.Background(), provider)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if endpoints.Authorization != provider.AuthorizationEndpoint {
		t.Fatalf("endpoints = %+v", endpoints)
	}
}
