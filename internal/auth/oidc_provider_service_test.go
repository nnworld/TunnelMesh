package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func newProviderService(t *testing.T, secrets SecretProvider) (*OIDCProviderService, *storage.DB) {
	t.Helper()
	db := newIdentityStore(t)
	service := NewOIDCProviderService(db, secrets, nil, []string{"https://tm.example.com"})
	return service, db
}

func validProviderInput() OIDCProviderInput {
	return OIDCProviderInput{
		Name:               "corpid",
		DisplayName:        "Corp IdP",
		Issuer:             "https://idp.example.com",
		ClientID:           "tunnelmesh-console",
		ClientSecret:       "sup3r-s3cr3t",
		Scopes:             []string{"openid", "profile", "email"},
		RedirectURI:        "https://tm.example.com/api/v1/auth/oidc/corpid/callback",
		IDTokenAlgs:        []string{"RS256"},
		UsernameClaim:      "preferred_username",
		RoleMappings:       []oidc.RoleMapping{{Claim: "groups", Value: "tm-admins", Role: "admin"}},
		DefaultRole:        "user",
		AutoCreateUsers:    boolPtr(true),
		AuthoritativeRoles: boolPtr(true),
		PublicListed:       boolPtr(true),
		Enabled:            boolPtr(true),
	}
}

func boolPtr(v bool) *bool { return &v }

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestOIDCProviderCreateValidatesEveryField(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		mutate func(*OIDCProviderInput)
		want   string
	}{
		{"name uppercase", func(in *OIDCProviderInput) { in.Name = "CorpID" }, "name must match"},
		{"name too short", func(in *OIDCProviderInput) { in.Name = "a" }, "name must match"},
		{"name leading dash", func(in *OIDCProviderInput) { in.Name = "-corp" }, "name must match"},
		{"name with slash", func(in *OIDCProviderInput) { in.Name = "corp/id" }, "name must match"},
		{"empty client id", func(in *OIDCProviderInput) { in.ClientID = "" }, "clientId is required"},
		{"http issuer", func(in *OIDCProviderInput) { in.Issuer = "http://idp.example.com" }, "issuer must use https"},
		{"relative issuer", func(in *OIDCProviderInput) { in.Issuer = "idp.example.com" }, "issuer must be an absolute URL"},
		{"http redirect", func(in *OIDCProviderInput) { in.RedirectURI = "http://tm.example.com/cb" }, "redirectUri must use https"},
		{"foreign redirect", func(in *OIDCProviderInput) { in.RedirectURI = "https://evil.example.com/cb" }, "not inside an allowed base"},
		{"missing openid scope", func(in *OIDCProviderInput) { in.Scopes = []string{"profile"} }, ""},
		{"none algorithm", func(in *OIDCProviderInput) { in.IDTokenAlgs = []string{"none"} }, "idTokenAlgs"},
		{"hs256 algorithm", func(in *OIDCProviderInput) { in.IDTokenAlgs = []string{"HS256"} }, "idTokenAlgs"},
		{"unknown algorithm", func(in *OIDCProviderInput) { in.IDTokenAlgs = []string{"RS999"} }, "idTokenAlgs"},
		{"bad default role", func(in *OIDCProviderInput) { in.DefaultRole = "superuser" }, "defaultRole must be admin or user"},
		{"bad mapping role", func(in *OIDCProviderInput) {
			in.RoleMappings = []oidc.RoleMapping{{Claim: "groups", Value: "x", Role: "root"}}
		}, "roleMappings[].role must be admin or user"},
		{"empty mapping claim", func(in *OIDCProviderInput) {
			in.RoleMappings = []oidc.RoleMapping{{Claim: "", Value: "x", Role: "admin"}}
		}, "roleMappings[].claim is required"},
		{"relative token endpoint", func(in *OIDCProviderInput) { in.TokenEndpoint = "/token" }, "tokenEndpoint must be an absolute http(s) URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh database per case keeps the "persisted nothing" assertion
			// meaningful; a shared one would report a previous case's row.
			service, _ := newProviderService(t, testSecretProvider(t))
			in := validProviderInput()
			tc.mutate(&in)
			err := func() error {
				_, err := service.Create(ctx, "admin-1", in, "")
				return err
			}()
			if !errors.Is(err, ErrOIDCProviderInvalid) {
				t.Fatalf("err = %v, want ErrOIDCProviderInvalid", err)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %q does not mention %q", err, tc.want)
			}
			page, err := service.List(ctx, "", 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 0 {
				t.Fatalf("invalid input persisted %d providers", len(page.Items))
			}
		})
	}
}

// TestOIDCProviderCreateNormalizesScopesAndAlgorithms covers the shape the stored
// row takes: openid first, duplicates removed, algorithms canonicalized.
func TestOIDCProviderCreateNormalizesScopesAndAlgorithms(t *testing.T) {
	service, _ := newProviderService(t, testSecretProvider(t))
	in := validProviderInput()
	in.Scopes = []string{"email", "openid", "email", " profile "}
	in.IDTokenAlgs = []string{"rs256", " EdDSA "}
	view, err := service.Create(context.Background(), "admin-1", in, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(view.Scopes, ",") != "openid,email,profile" {
		t.Fatalf("scopes = %v", view.Scopes)
	}
	if strings.Join(view.IDTokenAlgs, ",") != "RS256,EdDSA" {
		t.Fatalf("algorithms = %v", view.IDTokenAlgs)
	}
}

func TestOIDCProviderSecretIsStoredEncryptedAndNeverReturned(t *testing.T) {
	service, db := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasSecret {
		t.Fatal("hasSecret is false after storing a secret")
	}
	// The view is a distinct type with no secret field, so assert on its JSON.
	encoded := mustJSON(t, view)
	for _, leak := range []string{"sup3r-s3cr3t", "clientSecret", "ciphertext"} {
		if strings.Contains(encoded, leak) {
			t.Fatalf("provider view leaked %q: %s", leak, encoded)
		}
	}
	var row storage.OIDCProvider
	if err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.Get(ctx, view.ID)
		row = found
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.ClientSecretCiphertext, "sup3r-s3cr3t") {
		t.Fatal("secret stored in plaintext")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(row.ClientSecretCiphertext)
	if err != nil {
		t.Fatalf("ciphertext is not base64: %v", err)
	}
	if strings.Contains(string(ciphertext), "sup3r-s3cr3t") {
		t.Fatal("decodable ciphertext contained the plaintext")
	}
	if row.ClientSecretKeyID == "" {
		t.Fatal("key id was not recorded, so rotation cannot be tracked")
	}
}

func TestOIDCProviderCreateWithoutEncryptionKeyFailsClosed(t *testing.T) {
	service, _ := newProviderService(t, NewSecretProvider(nil))
	if _, err := service.Create(context.Background(), "admin-1", validProviderInput(), ""); !errors.Is(err, ErrSecretStorageUnavailable) {
		t.Fatalf("err = %v, want ErrSecretStorageUnavailable", err)
	}
	// A public client needs no secret and must still be creatable.
	in := validProviderInput()
	in.ClientSecret = ""
	if _, err := service.Create(context.Background(), "admin-1", in, ""); err != nil {
		t.Fatalf("public client create: %v", err)
	}
}

func TestOIDCProviderUpdateKeepsSecretWhenEmpty(t *testing.T) {
	service, db := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	before := storedProvider(t, db, view.ID)

	updated, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{DisplayName: "Renamed IdP"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Renamed IdP" {
		t.Fatalf("displayName = %q", updated.DisplayName)
	}
	after := storedProvider(t, db, view.ID)
	if after.ClientSecretCiphertext != before.ClientSecretCiphertext || after.ClientSecretNonce != before.ClientSecretNonce || after.ClientSecretKeyID != before.ClientSecretKeyID {
		t.Fatal("an empty secret replaced the stored secret")
	}
	// Untouched fields must survive a partial update.
	if after.ClientID != before.ClientID || after.Issuer != before.Issuer || after.RedirectURI != before.RedirectURI || !after.AutoCreateUsers || !after.AuthoritativeRoles {
		t.Fatalf("partial update clobbered fields: %+v", after)
	}

	rotated, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{ClientSecret: "n3w-s3cr3t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.HasSecret {
		t.Fatal("hasSecret false after rotation")
	}
	final := storedProvider(t, db, view.ID)
	if final.ClientSecretCiphertext == before.ClientSecretCiphertext {
		t.Fatal("rotation did not replace the ciphertext")
	}
	if strings.Contains(final.ClientSecretCiphertext, "n3w-s3cr3t") {
		t.Fatal("rotated secret stored in plaintext")
	}
}

func TestOIDCProviderUpdateRejectsNameChange(t *testing.T) {
	service, _ := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{Name: "othername"}, ""); !errors.Is(err, ErrOIDCProviderInvalid) {
		t.Fatalf("err = %v, want ErrOIDCProviderInvalid", err)
	}
	if _, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{Name: "corpid", DisplayName: "Same Name"}, ""); err != nil {
		t.Fatalf("unchanged name rejected: %v", err)
	}
}

func TestOIDCProviderUpdateRejectsInvalidValues(t *testing.T) {
	service, _ := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{Issuer: "http://insecure.example.com"}, ""); !errors.Is(err, ErrOIDCProviderInvalid) {
		t.Fatalf("http issuer accepted: %v", err)
	}
	if _, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{IDTokenAlgs: []string{"HS512"}}, ""); !errors.Is(err, ErrOIDCProviderInvalid) {
		t.Fatalf("HS512 accepted: %v", err)
	}
}

// TestOIDCProviderDeleteProtectsSSOOnlyAccounts covers the lockout path: removing
// the last enabled provider strands accounts that have no password.
func TestOIDCProviderDeleteProtectsSSOOnlyAccounts(t *testing.T) {
	service, db := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	// A second enabled provider makes deletion safe.
	other := validProviderInput()
	other.Name = "secondidp"
	other.RedirectURI = "https://tm.example.com/api/v1/auth/oidc/secondidp/callback"
	second, err := service.Create(ctx, "admin-1", other, "")
	if err != nil {
		t.Fatal(err)
	}
	seedExternalUser(t, db, "ext-1", "oidc-user", "user")
	if err := service.Delete(ctx, "admin-1", second.ID, ""); err != nil {
		t.Fatalf("delete with another provider enabled: %v", err)
	}
	if err := service.Delete(ctx, "admin-1", view.ID, ""); !errors.Is(err, ErrOIDCProviderInUse) {
		t.Fatalf("err = %v, want ErrOIDCProviderInUse", err)
	}
	// Disabling the last provider is the same lockout and must also be refused.
	if _, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{Enabled: boolPtr(false)}, ""); !errors.Is(err, ErrOIDCProviderInUse) {
		t.Fatalf("disable err = %v, want ErrOIDCProviderInUse", err)
	}
	// With no external-only account left, deletion is allowed.
	if err := db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		return repos.Users.Delete(ctx, "ext-1")
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, "admin-1", view.ID, ""); err != nil {
		t.Fatalf("delete after the dependent account was removed: %v", err)
	}
	if _, err := service.Get(ctx, view.ID); !errors.Is(err, ErrOIDCProviderNotFound) {
		t.Fatalf("err = %v, want ErrOIDCProviderNotFound", err)
	}
}

func TestOIDCProviderListPaginationAndPublicListing(t *testing.T) {
	service, _ := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	names := []string{"alpha", "beta", "gamma", "delta"}
	ids := map[string]string{}
	for _, name := range names {
		in := validProviderInput()
		in.Name = name
		in.RedirectURI = "https://tm.example.com/api/v1/auth/oidc/" + name + "/callback"
		in.PublicListed = boolPtr(name != "delta")
		view, err := service.Create(ctx, "admin-1", in, "")
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = view.ID
	}
	page, err := service.List(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page = %+v", page)
	}
	second, err := service.List(ctx, page.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 {
		t.Fatalf("second page = %+v", second)
	}
	seen := map[string]bool{}
	for _, item := range append(page.Items, second.Items...) {
		seen[item.Name] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Fatalf("provider %q missing from the paged listing", name)
		}
	}
	public, err := service.ListPublic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range public {
		if item.Name == "delta" {
			t.Fatal("a non-listed provider appeared in the public listing")
		}
		if item.DisplayName == "" {
			t.Fatalf("public view %+v has no display name", item)
		}
	}
	if len(public) != 3 {
		t.Fatalf("public listing has %d entries, want 3", len(public))
	}
	// The unauthenticated view must not carry configuration detail.
	encoded := mustJSON(t, public)
	for _, leak := range []string{"idp.example.com", "tunnelmesh-console", "sup3r-s3cr3t"} {
		if strings.Contains(encoded, leak) {
			t.Fatalf("public listing leaked %q", leak)
		}
	}
}

func TestOIDCProviderDuplicateNameRejected(t *testing.T) {
	service, _ := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	if _, err := service.Create(ctx, "admin-1", validProviderInput(), ""); err != nil {
		t.Fatal(err)
	}
	_, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if !errors.Is(err, ErrOIDCProviderInvalid) && !errors.Is(err, storage.ErrIdentityConflict) {
		t.Fatalf("err = %v", err)
	}
}

// TestOIDCProviderAuditRecordsFieldNamesOnly is the leak guard for the admin
// surface: the audit trail must show what changed without showing any value that
// could authenticate.
func TestOIDCProviderAuditRecordsFieldNamesOnly(t *testing.T) {
	service, db := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "admin-1", view.ID, OIDCProviderInput{ClientSecret: "n3w-s3cr3t", DisplayName: "Rotated"}, "idem-2"); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, "admin-1", view.ID, "idem-3"); err != nil {
		t.Fatal(err)
	}
	details := auditDetails(t, db)
	for _, secret := range []string{"sup3r-s3cr3t", "n3w-s3cr3t"} {
		if strings.Contains(details, secret) {
			t.Fatalf("audit details contain the secret %q", secret)
		}
	}
	// The audit listing is newest-first, so assert membership rather than order.
	recorded := map[string]bool{}
	for _, action := range auditActions(t, db) {
		recorded[action] = true
	}
	for _, action := range []string{"auth.oidc.provider.create", "auth.oidc.provider.update", "auth.oidc.provider.delete"} {
		if !recorded[action] {
			t.Fatalf("missing audit action %q (recorded: %v)", action, recorded)
		}
	}
	if !strings.Contains(details, "clientSecret") {
		t.Fatalf("update audit did not record that the secret field changed: %s", details)
	}
}

func TestOIDCProviderResolveRelyingParty(t *testing.T) {
	service, _ := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := service.ResolveRelyingParty(ctx, "corpid")
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != view.ID || provider.Issuer != "https://idp.example.com" || provider.ClientID != "tunnelmesh-console" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.ClientSecret.IsZero() {
		t.Fatal("sealed secret was not attached")
	}
	if provider.Secret != nil {
		t.Fatal("plaintext secret closure was attached to the resolved provider")
	}
	if len(provider.RoleMappings) != 1 || provider.RoleMappings[0].Role != "admin" {
		t.Fatalf("role mappings = %+v", provider.RoleMappings)
	}
	if provider.Scopes[0] != "openid" {
		t.Fatalf("scopes = %v", provider.Scopes)
	}
	if _, err := service.ResolveRelyingParty(ctx, "missing"); !errors.Is(err, ErrOIDCProviderNotFound) {
		t.Fatalf("err = %v, want ErrOIDCProviderNotFound", err)
	}
	// A disabled provider must not resolve for a login even though the row exists.
	disabled := validProviderInput()
	disabled.Name = "disabledidp"
	disabled.RedirectURI = "https://tm.example.com/api/v1/auth/oidc/disabledidp/callback"
	disabled.Enabled = boolPtr(false)
	other := validProviderInput()
	other.Name = "keepsidp"
	other.RedirectURI = "https://tm.example.com/api/v1/auth/oidc/keepsidp/callback"
	if _, err := service.Create(ctx, "admin-1", other, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, "admin-1", disabled, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveRelyingParty(ctx, "disabledidp"); !errors.Is(err, ErrOIDCProviderDisabled) {
		t.Fatalf("err = %v, want ErrOIDCProviderDisabled", err)
	}
}

func TestOIDCProviderResolveWithoutEncryptionKeyFailsClosed(t *testing.T) {
	db := newIdentityStore(t)
	seeded := NewOIDCProviderService(db, testSecretProvider(t), nil, []string{"https://tm.example.com"})
	ctx := context.Background()
	if _, err := seeded.Create(ctx, "admin-1", validProviderInput(), ""); err != nil {
		t.Fatal(err)
	}
	// A restart without TUNNELMESH_TOKEN_ENCRYPTION_KEY must refuse to log
	// anybody in through this provider rather than silently become a public client.
	keyless := NewOIDCProviderService(db, NewSecretProvider(nil), nil, []string{"https://tm.example.com"})
	if _, err := keyless.ResolveRelyingParty(ctx, "corpid"); !errors.Is(err, ErrSecretStorageUnavailable) {
		t.Fatalf("err = %v, want ErrSecretStorageUnavailable", err)
	}
}

func TestOIDCProviderTestReportsWithoutSecrets(t *testing.T) {
	service, db := newProviderService(t, testSecretProvider(t))
	ctx := context.Background()
	view, err := service.Create(ctx, "admin-1", validProviderInput(), "")
	if err != nil {
		t.Fatal(err)
	}
	// No relying party is wired in this unit test, so the report must say so
	// without inventing a success.
	report, err := service.Test(ctx, "admin-1", view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.DiscoveryOK || report.JWKSOK {
		t.Fatalf("report claimed success without a relying party: %+v", report)
	}
	if report.Error == "" {
		t.Fatal("report carried no error")
	}
	encoded := mustJSON(t, report)
	if strings.Contains(encoded, "sup3r-s3cr3t") {
		t.Fatalf("test report leaked the secret: %s", encoded)
	}
	actions := auditActions(t, db)
	found := false
	for _, action := range actions {
		if action == "auth.oidc.provider.test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no test audit row: %v", actions)
	}
	if _, err := service.Test(ctx, "admin-1", "missing"); !errors.Is(err, ErrOIDCProviderNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthPolicyGetSeedsFromDefaults(t *testing.T) {
	db := newIdentityStore(t)
	defaults := DefaultAuthSettings(storage.MFAModeOptional, true, false, 7*24*time.Hour, 5, 12*time.Hour)
	service := NewAuthPolicyService(db, defaults)
	settings, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.MFAMode != storage.MFAModeOptional || settings.MaxTrustedDevices != 5 || settings.SessionTokenTTLSeconds != 43200 {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.AllowTrustedDeviceBypass {
		t.Fatal("seed did not honour the configured bypass value")
	}
	// A second read must return the stored row, not re-seed.
	again, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.MFAMode != settings.MFAMode {
		t.Fatalf("settings changed between reads: %+v vs %+v", settings, again)
	}
}

func TestAuthPolicyUpdateValidation(t *testing.T) {
	db := newIdentityStore(t)
	service := NewAuthPolicyService(db, DefaultAuthSettings(storage.MFAModeDisabled, true, true, 0, 0, 0))
	ctx := context.Background()
	valid := AuthPolicyInput{MFAMode: storage.MFAModeRequired, DeviceTrustEnabled: true, DeviceTrustTTLSeconds: 86400, AllowTrustedDeviceBypass: true, MaxTrustedDevices: 10, SessionTokenTTLSeconds: 3600}
	if _, err := service.Update(ctx, "admin-1", valid); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*AuthPolicyInput)
	}{
		{"bad mode", func(in *AuthPolicyInput) { in.MFAMode = "sometimes" }},
		{"empty mode", func(in *AuthPolicyInput) { in.MFAMode = "" }},
		{"ttl too small", func(in *AuthPolicyInput) { in.DeviceTrustTTLSeconds = 60 }},
		{"ttl too large", func(in *AuthPolicyInput) { in.DeviceTrustTTLSeconds = MaxDeviceTrustTTLSeconds + 1 }},
		{"too few devices", func(in *AuthPolicyInput) { in.MaxTrustedDevices = 0 }},
		{"too many devices", func(in *AuthPolicyInput) { in.MaxTrustedDevices = MaxTrustedDevices + 1 }},
		{"negative session ttl", func(in *AuthPolicyInput) { in.SessionTokenTTLSeconds = -1 }},
		{"bypass without trust", func(in *AuthPolicyInput) { in.DeviceTrustEnabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.mutate(&in)
			if _, err := service.Update(ctx, "admin-1", in); !errors.Is(err, ErrAuthPolicyInvalid) {
				t.Fatalf("err = %v, want ErrAuthPolicyInvalid", err)
			}
		})
	}
	current, err := service.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.MFAMode != storage.MFAModeRequired || current.DeviceTrustTTLSeconds != 86400 {
		t.Fatalf("an invalid update changed the stored policy: %+v", current)
	}
}

func TestAuthPolicyUpdateWritesAuditAndKeepsPerUserOverride(t *testing.T) {
	db := newIdentityStore(t)
	service := NewAuthPolicyService(db, DefaultAuthSettings(storage.MFAModeDisabled, true, true, 0, 0, 0))
	ctx := context.Background()
	user := seedLocalUser(t, db, "u1", "alice", "admin", "pw")
	setMFARequired(t, db, user.ID, true)

	if _, err := service.Update(ctx, "admin-1", AuthPolicyInput{MFAMode: storage.MFAModeRequired, DeviceTrustEnabled: true, DeviceTrustTTLSeconds: 86400, AllowTrustedDeviceBypass: true, MaxTrustedDevices: 10}); err != nil {
		t.Fatal(err)
	}
	// Turning the global policy back off must not silently un-protect the account
	// an administrator explicitly flagged.
	if _, err := service.Update(ctx, "admin-1", AuthPolicyInput{MFAMode: storage.MFAModeDisabled, DeviceTrustEnabled: true, DeviceTrustTTLSeconds: 86400, AllowTrustedDeviceBypass: true, MaxTrustedDevices: 10}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MFAMode != storage.MFAModeDisabled {
		t.Fatalf("mode = %q", settings.MFAMode)
	}
	current, err := db.Users().Get(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if policy := ResolvePolicy(settings, current, false); !policy.Required {
		t.Fatalf("per-account override was lost: %+v", policy)
	}
	details := auditDetails(t, db)
	if !strings.Contains(details, "auth.policy.update") && !strings.Contains(auditActionsString(t, db), "auth.policy.update") {
		t.Fatal("no auth.policy.update audit row")
	}
	if !strings.Contains(details, "mfaMode") {
		t.Fatalf("audit did not name the changed field: %s", details)
	}
}

func auditActionsString(t *testing.T, db *storage.DB) string {
	t.Helper()
	return strings.Join(auditActions(t, db), ",")
}

func storedProvider(t *testing.T, db *storage.DB, id string) storage.OIDCProvider {
	t.Helper()
	var row storage.OIDCProvider
	err := db.IdentityRead(context.Background(), func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.Get(context.Background(), id)
		row = found
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}
