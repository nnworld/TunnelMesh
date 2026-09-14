package proxyentry_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

type stubSecrets struct {
	secret proxyentry.CredentialSecret
	err    error
	// mu guards calls: Authorize is exercised concurrently below, and an
	// unsynchronized counter here would be a race in the test rather than in
	// the code under test.
	mu    sync.Mutex
	calls int
}

func (s *stubSecrets) ProxyBasicSecret(context.Context, string) (proxyentry.CredentialSecret, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.secret, s.err
}

func (s *stubSecrets) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestAuthenticatorModes(t *testing.T) {
	secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
	auth := proxyentry.NewAuthenticator(secrets, 5)
	route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	ip := net.ParseIP("11.71.85.7")

	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
	if err := auth.Authorize(context.Background(), proxyentry.Route{ID: "r2", AuthMode: proxyentry.AuthModeNone}, ip, ""); err != nil {
		t.Fatalf("none mode must skip auth: %v", err)
	}
	// A none-mode route must accept a wrong credential too: it does not
	// authenticate at all, and rejecting a stray header would break clients
	// that send cached credentials to every proxy.
	if err := auth.Authorize(context.Background(), proxyentry.Route{ID: "r3", AuthMode: proxyentry.AuthModeNone}, ip, basic("demo", "wrong")); err != nil {
		t.Fatalf("none mode must ignore credentials: %v", err)
	}
	if got := secrets.callCount(); got != 1 {
		t.Fatalf("none mode must not touch the secret store, calls=%d", got)
	}
	if err := auth.Authorize(context.Background(), route, ip, ""); !errors.Is(err, proxyentry.ErrAuthRequired) {
		t.Fatalf("missing header = %v", err)
	}
	if err := auth.Authorize(context.Background(), route, ip, "Bearer abc"); !errors.Is(err, proxyentry.ErrAuthRequired) {
		t.Fatalf("wrong scheme = %v", err)
	}
	if err := auth.Authorize(context.Background(), route, ip, "Basic !!!not-base64!!!"); !errors.Is(err, proxyentry.ErrAuthRequired) {
		t.Fatalf("bad base64 = %v", err)
	}
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "wrong")); !errors.Is(err, proxyentry.ErrAuthFailed) {
		t.Fatalf("wrong password = %v", err)
	}
	if err := auth.Authorize(context.Background(), route, ip, basic("DEMO", "s3cret")); !errors.Is(err, proxyentry.ErrAuthFailed) {
		t.Fatalf("username must match exactly, got %v", err)
	}
}

func TestAuthenticatorAllowsColonInPassword(t *testing.T) {
	// RFC 7617 forbids a colon in the userid but allows it in the password, and
	// the 407 challenge advertises charset="UTF-8". Splitting on "exactly one
	// colon" would lock users out of a legal password.
	secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "p:a:s:s"}}
	auth := proxyentry.NewAuthenticator(secrets, 5)
	route := proxyentry.Route{ID: "rc", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	if err := auth.Authorize(context.Background(), route, net.ParseIP("10.0.0.2"), basic("demo", "p:a:s:s")); err != nil {
		t.Fatalf("password containing colons rejected: %v", err)
	}
	if err := auth.Authorize(context.Background(), route, net.ParseIP("10.0.0.2"), "Basic "+base64.StdEncoding.EncodeToString([]byte("nocolon"))); !errors.Is(err, proxyentry.ErrAuthRequired) {
		t.Fatalf("credential without a colon separator = %v, want ErrAuthRequired", err)
	}
}

func TestAuthenticatorSecretStoreFailure(t *testing.T) {
	secrets := &stubSecrets{err: proxyentry.ErrSecretStoreUnavailable}
	auth := proxyentry.NewAuthenticator(secrets, 5)
	route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	err := auth.Authorize(context.Background(), route, net.ParseIP("10.0.0.1"), basic("demo", "s3cret"))
	if !errors.Is(err, proxyentry.ErrSecretUnavailable) {
		t.Fatalf("got %v", err)
	}
	if auth.Attempts("r1", "10.0.0.1") != 0 {
		t.Fatal("store failures must not count against the user")
	}
}

func TestAuthenticatorBackoff(t *testing.T) {
	secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
	auth := proxyentry.NewAuthenticator(secrets, 3)
	now := time.Unix(1_800_000_000, 0)
	auth.SetClock(func() time.Time { return now })
	route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	ip := net.ParseIP("10.0.0.9")
	for i := 0; i < 3; i++ {
		if err := auth.Authorize(context.Background(), route, ip, basic("demo", "bad")); !errors.Is(err, proxyentry.ErrAuthFailed) {
			t.Fatalf("attempt %d = %v", i, err)
		}
	}
	callsBefore := secrets.callCount()
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); !errors.Is(err, proxyentry.ErrAuthBackoff) {
		t.Fatalf("after threshold got %v", err)
	}
	if secrets.callCount() != callsBefore {
		t.Fatal("backoff must short-circuit before comparing passwords")
	}
	now = now.Add(31 * time.Second)
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); err != nil {
		t.Fatalf("after 30s backoff got %v", err)
	}
	if auth.Attempts("r1", "10.0.0.9") != 0 {
		t.Fatal("success must reset the counter")
	}
}

func TestAuthenticatorBackoffGrowsAndCaps(t *testing.T) {
	secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
	auth := proxyentry.NewAuthenticator(secrets, 1)
	now := time.Unix(1_800_000_000, 0)
	auth.SetClock(func() time.Time { return now })
	route := proxyentry.Route{ID: "rb", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	ip := net.ParseIP("10.0.0.11")

	// threshold=1: the first failure starts a 30s backoff, the second doubles
	// it to 60s. Verified by advancing 31s, which clears the first but not the
	// second.
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "bad")); !errors.Is(err, proxyentry.ErrAuthFailed) {
		t.Fatalf("first failure = %v", err)
	}
	now = now.Add(31 * time.Second)
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "bad")); !errors.Is(err, proxyentry.ErrAuthFailed) {
		t.Fatalf("second failure = %v", err)
	}
	now = now.Add(31 * time.Second)
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); !errors.Is(err, proxyentry.ErrAuthBackoff) {
		t.Fatalf("backoff must have doubled to 60s, got %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); err != nil {
		t.Fatalf("after the doubled window got %v", err)
	}
}

func TestAuthenticatorBackoffIsScopedPerRouteAndClient(t *testing.T) {
	secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
	auth := proxyentry.NewAuthenticator(secrets, 2)
	now := time.Unix(1_800_000_000, 0)
	auth.SetClock(func() time.Time { return now })
	basicRoute := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	otherRoute := proxyentry.Route{ID: "r2", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	attacker := net.ParseIP("10.0.0.20")
	victim := net.ParseIP("10.0.0.21")

	for i := 0; i < 2; i++ {
		if err := auth.Authorize(context.Background(), basicRoute, attacker, basic("demo", "bad")); !errors.Is(err, proxyentry.ErrAuthFailed) {
			t.Fatalf("attempt %d = %v", i, err)
		}
	}
	if err := auth.Authorize(context.Background(), basicRoute, attacker, basic("demo", "s3cret")); !errors.Is(err, proxyentry.ErrAuthBackoff) {
		t.Fatalf("attacker should be in backoff, got %v", err)
	}
	// One client's brute force must not lock out another client on the same
	// route, nor the same client on a different route.
	if err := auth.Authorize(context.Background(), basicRoute, victim, basic("demo", "s3cret")); err != nil {
		t.Fatalf("another client was locked out: %v", err)
	}
	if err := auth.Authorize(context.Background(), otherRoute, attacker, basic("demo", "s3cret")); err != nil {
		t.Fatalf("another route was locked out: %v", err)
	}
}

func TestAuthenticatorMissingCredentialID(t *testing.T) {
	auth := proxyentry.NewAuthenticator(&stubSecrets{}, 5)
	route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic}
	if err := auth.Authorize(context.Background(), route, net.ParseIP("10.0.0.1"), basic("demo", "x")); !errors.Is(err, proxyentry.ErrAuthFailed) {
		t.Fatalf("missing credentialId = %v", err)
	}
}

func TestAuthenticatorConcurrentAuthorize(t *testing.T) {
	secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
	auth := proxyentry.NewAuthenticator(secrets, 5)
	route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			ip := net.ParseIP("10.0.0.30").To4()
			ip[3] = byte(i)
			header := basic("demo", "s3cret")
			if i%2 == 0 {
				header = basic("demo", "bad")
			}
			_ = auth.Authorize(context.Background(), route, ip, header)
		}(i)
	}
	for i := 0; i < 16; i++ {
		<-done
	}
}
