package oidc

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testProvider() Provider {
	return Provider{
		Name:          "corpid",
		Issuer:        "https://idp.example.com",
		ClientID:      "tunnelmesh",
		UsernameClaim: "preferred_username",
		DefaultRole:   "user",
	}
}

// TestResolveIdentityUsernamePrecedence covers the three-stage fallback: the
// configured claim, then email, then a deterministic provider-scoped name.
func TestResolveIdentityUsernamePrecedence(t *testing.T) {
	provider := testProvider()

	got, _, err := ResolveIdentity(provider, Claims{Subject: "s-1", Username: "Alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alice" {
		t.Fatalf("username = %q, want the configured claim lowercased", got)
	}

	got, _, err = ResolveIdentity(provider, Claims{Subject: "s-1", Username: "  ", Email: " Bob@Example.COM "})
	if err != nil {
		t.Fatal(err)
	}
	if got != "bob_example.com" {
		t.Fatalf("username = %q, want the sanitized email fallback", got)
	}
	if !ValidUsername(got) {
		t.Fatalf("email-derived username %q violates the account rule", got)
	}

	got, _, err = ResolveIdentity(provider, Claims{Subject: "00000000-1111-2222-3333-444455556666"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "corpid-") {
		t.Fatalf("username = %q, want a provider-scoped fallback", got)
	}
	if !ValidUsername(got) {
		t.Fatalf("fallback username %q violates the 3-64 character rule", got)
	}
	// The fallback must be stable, otherwise every login would create a new
	// account for the same subject.
	again, _, err := ResolveIdentity(provider, Claims{Subject: "00000000-1111-2222-3333-444455556666"})
	if err != nil || again != got {
		t.Fatalf("fallback is not stable: %q vs %q (err %v)", got, again, err)
	}
	different, _, err := ResolveIdentity(provider, Claims{Subject: "another-subject"})
	if err != nil || different == got {
		t.Fatalf("distinct subjects collided: %q", different)
	}
}

// TestResolveIdentityFallbackSanitizesProviderName keeps a provider name with
// characters the account rule forbids from producing an unusable username.
func TestResolveIdentityFallbackSanitizesProviderName(t *testing.T) {
	for _, name := range []string{"Corp IdP!", "", "a", "Ünïcödé", strings.Repeat("x", 80)} {
		provider := testProvider()
		provider.Name = name
		got, _, err := ResolveIdentity(provider, Claims{Subject: "sub-1"})
		if err != nil {
			t.Fatalf("name %q: %v", name, err)
		}
		if !ValidUsername(got) {
			t.Fatalf("name %q produced invalid username %q", name, got)
		}
	}
}

func TestResolveIdentityRejectsMissingSubject(t *testing.T) {
	provider := testProvider()
	// No subject means no stable identity, and inventing one would let a claim
	// change silently re-provision an account.
	if _, _, err := ResolveIdentity(provider, Claims{}); !errors.Is(err, ErrClaimMissing) {
		t.Fatalf("err = %v, want ErrClaimMissing", err)
	}
}

// TestResolveRoleMappingOrder covers first-match-wins, which is what makes an
// admin rule placed before a broad user rule behave predictably.
func TestResolveRoleMappingOrder(t *testing.T) {
	provider := testProvider()
	provider.RoleMappings = []RoleMapping{
		{Claim: "groups", Value: "tunnelmesh-admins", Role: "admin"},
		{Claim: "groups", Value: "everyone", Role: "user"},
	}
	claims := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"groups": []any{"everyone", "tunnelmesh-admins"}}}
	_, role, err := ResolveIdentity(provider, claims)
	if err != nil {
		t.Fatal(err)
	}
	if role != "admin" {
		t.Fatalf("role = %q, want admin from the first matching rule", role)
	}

	reversed := testProvider()
	reversed.RoleMappings = []RoleMapping{
		{Claim: "groups", Value: "everyone", Role: "user"},
		{Claim: "groups", Value: "tunnelmesh-admins", Role: "admin"},
	}
	_, role, err = ResolveIdentity(reversed, claims)
	if err != nil {
		t.Fatal(err)
	}
	if role != "user" {
		t.Fatalf("role = %q, want user when the broad rule is first", role)
	}
}

func TestResolveRoleMatchesScalarAndArrayClaims(t *testing.T) {
	provider := testProvider()
	provider.RoleMappings = []RoleMapping{{Claim: "roles", Value: "tm-viewer", Role: "user"}}

	stringClaim := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"roles": "tm-viewer"}}
	if _, role, err := ResolveIdentity(provider, stringClaim); err != nil || role != "user" {
		t.Fatalf("string claim: role = %q err = %v", role, err)
	}
	arrayClaim := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"roles": []any{"other", "tm-viewer"}}}
	if _, role, err := ResolveIdentity(provider, arrayClaim); err != nil || role != "user" {
		t.Fatalf("array claim: role = %q err = %v", role, err)
	}
	// Case differences between an IdP group name and the configured value must
	// not silently drop a user's role.
	mixed := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"roles": []any{"TM-Viewer"}}}
	if _, role, err := ResolveIdentity(provider, mixed); err != nil || role != "user" {
		t.Fatalf("mixed case: role = %q err = %v", role, err)
	}
}

// TestResolveRoleUnknownClaimIsSkipped keeps a provider that stops releasing one
// claim from breaking every login.
func TestResolveRoleUnknownClaimIsSkipped(t *testing.T) {
	provider := testProvider()
	provider.DefaultRole = "user"
	provider.RoleMappings = []RoleMapping{
		{Claim: "claim-that-does-not-exist", Value: "x", Role: "admin"},
		{Claim: "groups", Value: "tunnelmesh-admins", Role: "admin"},
	}
	claims := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"groups": []any{"tunnelmesh-admins"}}}
	if _, role, err := ResolveIdentity(provider, claims); err != nil || role != "admin" {
		t.Fatalf("role = %q err = %v", role, err)
	}
}

func TestResolveRoleInvalidRoleIsFatal(t *testing.T) {
	provider := testProvider()
	provider.RoleMappings = []RoleMapping{{Claim: "groups", Value: "x", Role: "superuser"}}
	claims := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"groups": "x"}}
	if _, _, err := ResolveIdentity(provider, claims); !errors.Is(err, ErrRoleMappingInvalid) {
		t.Fatalf("err = %v, want ErrRoleMappingInvalid", err)
	}
	// A rule with an empty claim is configuration noise, not an error.
	provider.RoleMappings = []RoleMapping{{Claim: "", Value: "x", Role: "admin"}}
	if _, role, err := ResolveIdentity(provider, claims); err != nil || role != "user" {
		t.Fatalf("role = %q err = %v", role, err)
	}
}

func TestResolveRoleFallsBackToDefault(t *testing.T) {
	provider := testProvider()
	provider.DefaultRole = "admin"
	provider.RoleMappings = []RoleMapping{{Claim: "groups", Value: "nope", Role: "user"}}
	claims := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"groups": "other"}}
	if _, role, err := ResolveIdentity(provider, claims); err != nil || role != "admin" {
		t.Fatalf("role = %q err = %v", role, err)
	}
	// An unset or invalid default must degrade to the least privileged role.
	for _, invalid := range []string{"", "root", "ADMIN "} {
		provider.DefaultRole = invalid
		if _, role, err := ResolveIdentity(provider, claims); err != nil {
			t.Fatal(err)
		} else if invalid == "ADMIN " {
			if role != "admin" {
				t.Fatalf("default %q gave role %q", invalid, role)
			}
		} else if role != "user" {
			t.Fatalf("default %q gave role %q, want user", invalid, role)
		}
	}
}

func TestResolveRoleNumericClaim(t *testing.T) {
	provider := testProvider()
	provider.RoleMappings = []RoleMapping{{Claim: "clearance", Value: "5", Role: "admin"}}
	claims := Claims{Subject: "s", Username: "alice", Raw: map[string]any{"clearance": float64(5)}}
	if _, role, err := ResolveIdentity(provider, claims); err != nil || role != "admin" {
		t.Fatalf("role = %q err = %v", role, err)
	}
}

// TestUsernameClaimSelectionEndToEnd proves the configured claim is the one read
// out of the real id_token, not just out of a hand-built Claims value.
func TestUsernameClaimSelectionEndToEnd(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	base := idp.validClaims(map[string]any{
		"preferred_username": "preferred-name",
		"nickname":           "nickname-value",
		"email":              "mail@example.com",
	})
	token := idp.signToken(t, "RS256", idp.rsaKid, base)

	// Claims.Username keeps the raw claim value; ResolveIdentity is what maps it
	// onto the account rule, so the two expectations differ for an email.
	for _, tc := range []struct {
		claim    string
		raw      string
		resolved string
	}{
		{"preferred_username", "preferred-name", "preferred-name"},
		{"nickname", "nickname-value", "nickname-value"},
		{"email", "mail@example.com", "mail_example.com"},
	} {
		provider := idp.provider
		provider.UsernameClaim = tc.claim
		claims, err := idp.rp.VerifyIDToken(ctx, provider, token, "test-nonce", "")
		if err != nil {
			t.Fatalf("claim %s: %v", tc.claim, err)
		}
		if claims.Username != tc.raw {
			t.Fatalf("claim %s gave raw username %q, want %q", tc.claim, claims.Username, tc.raw)
		}
		username, _, err := ResolveIdentity(provider, claims)
		if err != nil {
			t.Fatal(err)
		}
		if username != tc.resolved {
			t.Fatalf("claim %s resolved to %q, want %q", tc.claim, username, tc.resolved)
		}
		if !ValidUsername(username) {
			t.Fatalf("resolved username %q violates the account rule", username)
		}
	}

	// An unset claim falls back to preferred_username by convention.
	provider := idp.provider
	provider.UsernameClaim = ""
	claims, err := idp.rp.VerifyIDToken(ctx, provider, token, "test-nonce", "")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Username != "preferred-name" {
		t.Fatalf("default claim gave %q", claims.Username)
	}

	// A configured claim the IdP does not release falls back to email.
	provider.UsernameClaim = "claim-the-idp-never-sends"
	claims, err = idp.rp.VerifyIDToken(ctx, provider, token, "test-nonce", "")
	if err != nil {
		t.Fatal(err)
	}
	username, _, err := ResolveIdentity(provider, claims)
	if err != nil {
		t.Fatal(err)
	}
	if username != "mail_example.com" {
		t.Fatalf("username = %q, want the sanitized email fallback", username)
	}
}

func TestClaimReadersTolerateWrongTypes(t *testing.T) {
	raw := map[string]any{
		"string":         "value",
		"number":         float64(7),
		"bool":           true,
		"boolStringTrue": "TRUE",
		"boolStringNo":   "no",
		"object":         map[string]any{"a": 1},
	}
	if got := stringClaim(raw, "string"); got != "value" {
		t.Fatalf("stringClaim = %q", got)
	}
	if got := stringClaim(raw, "number"); got != "" {
		t.Fatalf("non-string claim returned %q", got)
	}
	if got := stringClaim(raw, "absent"); got != "" {
		t.Fatalf("absent claim returned %q", got)
	}
	if !boolClaim(raw, "bool") || !boolClaim(raw, "boolStringTrue") {
		t.Fatal("true claim read as false")
	}
	if boolClaim(raw, "boolStringNo") || boolClaim(raw, "object") || boolClaim(raw, "absent") {
		t.Fatal("non-true claim read as true")
	}
}

func TestClaimMatchesEdgeCases(t *testing.T) {
	if claimMatches([]any{float64(3)}, "3") != true {
		t.Fatal("numeric array member did not match")
	}
	if claimMatches(true, "true") != true {
		t.Fatal("bool claim did not match")
	}
	if claimMatches(nil, "") {
		t.Fatal("nil claim matched")
	}
	if claimMatches(map[string]any{"a": 1}, "a") {
		t.Fatal("object claim matched a scalar expectation")
	}
}
