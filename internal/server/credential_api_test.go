package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const validPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB3VnH4xLmFhZ0l6c2V0dGVzdHB1YmxpY2tleQ=="

func TestCredentialAPILifecycleAndAuthorization(t *testing.T) {
	api, admin, user := apiTestServer(t)
	token := apiToken(t, api, user.Username, "alice-pass")
	adminToken := apiToken(t, api, admin.Username, "admin-pass")
	h := api.Handler()

	if r := apiJSON(t, h, http.MethodGet, "/api/v1/credentials", "", "", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", r.Code)
	}
	create := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "credential-key", map[string]any{
		"name": "deploy-key", "type": "ssh_public_key", "publicKey": validPublicKey, "enabled": true,
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	var created envelope
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	replay := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "credential-key", map[string]any{
		"name": "deploy-key", "type": "ssh_public_key", "publicKey": validPublicKey, "enabled": true,
	})
	if replay.Code != http.StatusCreated || replay.Body.String() != create.Body.String() {
		t.Fatalf("idempotent replay = %d/%s", replay.Code, replay.Body.String())
	}
	invalid := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "invalid-key", map[string]any{
		"name": "bad-key", "type": "ssh_public_key", "publicKey": "not-a-key", "enabled": true,
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid key status = %d: %s", invalid.Code, invalid.Body.String())
	}
	detail := apiJSON(t, h, http.MethodGet, "/api/v1/credentials/"+created.Data.ID, token, "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d", detail.Code)
	}
	other := apiJSON(t, h, http.MethodGet, "/api/v1/credentials/"+created.Data.ID, adminToken, "", nil)
	if other.Code != http.StatusOK {
		t.Fatalf("admin detail status = %d: %s", other.Code, other.Body.String())
	}
	update := apiJSON(t, h, http.MethodPatch, "/api/v1/credentials/"+created.Data.ID, token, "", map[string]any{"name": "prod-key"})
	if update.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", update.Code, update.Body.String())
	}
	if r := apiJSON(t, h, http.MethodDelete, "/api/v1/credentials/"+created.Data.ID, token, "", nil); r.Code != http.StatusOK {
		t.Fatalf("delete status = %d", r.Code)
	}
	if r := apiJSON(t, h, http.MethodPost, "/api/v1/credentials/"+created.Data.ID+"/restore", token, "", nil); r.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", r.Code, r.Body.String())
	}
}

func TestCredentialAPIPagination(t *testing.T) {
	api, _, user := apiTestServer(t)
	token := apiToken(t, api, user.Username, "alice-pass")
	h := api.Handler()
	for i := 0; i < 3; i++ {
		body := map[string]any{"name": "key-" + string(rune('a'+i)), "type": "ssh_public_key", "publicKey": validPublicKey, "enabled": true}
		if r := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "", body); r.Code != http.StatusCreated {
			t.Fatalf("create key status = %d: %s", r.Code, r.Body.String())
		}
	}
	first := apiJSON(t, h, http.MethodGet, "/api/v1/credentials?limit=2", token, "", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d", first.Code)
	}
	var page pageResponse
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 2 || !page.Data.HasMore || page.Data.NextCursor == "" {
		t.Fatalf("first page = %+v", page.Data)
	}
	next := apiJSON(t, h, http.MethodGet, "/api/v1/credentials?limit=2&cursor="+page.Data.NextCursor, token, "", nil)
	var nextPage pageResponse
	if err := json.Unmarshal(next.Body.Bytes(), &nextPage); err != nil {
		t.Fatal(err)
	}
	if len(nextPage.Data.Items) != 1 || nextPage.Data.HasMore {
		t.Fatalf("next page = %+v", nextPage.Data)
	}
}

func TestCredentialAPIExtractsPublicKeyWithoutPersistingPrivateKey(t *testing.T) {
	api, _, user := apiTestServer(t)
	token := apiToken(t, api, user.Username, "alice-pass")
	handler := api.Handler()
	privateKey, expected := testEd25519PrivateKeyPEM(t, "")
	request := map[string]any{"privateKey": privateKey}
	response := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials/extract-ssh-public-key", token, "", request)
	if response.Code != http.StatusOK {
		t.Fatalf("extract status = %d: %s", response.Code, response.Body.String())
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	var payload struct {
		Data struct {
			PublicKey   string `json:"publicKey"`
			Fingerprint string `json:"fingerprint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.PublicKey != expected || payload.Data.Fingerprint == "" {
		t.Fatalf("payload = %+v, want %q", payload.Data, expected)
	}
	credentials := apiJSON(t, handler, http.MethodGet, "/api/v1/credentials", token, "", nil)
	var credentialPage pageResponse
	if err := json.Unmarshal(credentials.Body.Bytes(), &credentialPage); err != nil {
		t.Fatal(err)
	}
	if len(credentialPage.Data.Items) != 0 {
		t.Fatalf("extraction unexpectedly created credential: %+v", credentialPage.Data.Items)
	}
	audits, err := api.DB.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits.Items {
		if strings.Contains(audit.Details, "privateKey") || strings.Contains(audit.Details, privateKey) {
			t.Fatalf("private key material reached audit log: %+v", audit)
		}
	}
}

func TestCredentialAPIExtractRequiresAuthenticationAndValidInput(t *testing.T) {
	api, _, user := apiTestServer(t)
	token := apiToken(t, api, user.Username, "alice-pass")
	handler := api.Handler()
	unauthenticated := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials/extract-ssh-public-key", "", "", map[string]any{"privateKey": "invalid"})
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.Code)
	}
	invalid := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials/extract-ssh-public-key", token, "", map[string]any{"privateKey": "invalid"})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d: %s", invalid.Code, invalid.Body.String())
	}
	if cacheControl := invalid.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("error Cache-Control = %q, want no-store", cacheControl)
	}
}

type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
}

type pageResponse struct {
	Code int `json:"code"`
	Data struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	} `json:"data"`
}

// setCredentialSecretStore installs a deterministic AES-GCM store so credential
// secret handling can be exercised without touching the process environment.
func setCredentialSecretStore(t *testing.T, api *API) {
	t.Helper()
	store, err := auth.NewSecretStore(base64.RawStdEncoding.EncodeToString(make([]byte, 32)), "api-test-key")
	if err != nil {
		t.Fatal(err)
	}
	api.credentialService.SetSecretStore(store)
}

func TestCredentialAPIStoresSecretWithoutEchoingIt(t *testing.T) {
	api, _, user := apiTestServer(t)
	setCredentialSecretStore(t, api)
	token := apiToken(t, api, user.Username, "alice-pass")
	handler := api.Handler()

	create := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials", token, "cred-password", map[string]any{
		"name": "web-host", "type": "password", "enabled": true,
		"secret": map[string]any{"password": "s3cr3t-password"},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "s3cr3t-password") {
		t.Fatal("create response echoed the plaintext password")
	}
	var created struct {
		Data struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			PublicKey string `json:"publicKey"`
			HasSecret bool   `json:"hasSecret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Data.HasSecret || created.Data.Type != "password" || created.Data.PublicKey != "" {
		t.Fatalf("created credential = %+v", created.Data)
	}
	if strings.Contains(create.Body.String(), "secretCiphertext") || strings.Contains(create.Body.String(), "secretNonce") {
		t.Fatalf("create response leaked secret columns: %s", create.Body.String())
	}

	detail := apiJSON(t, handler, http.MethodGet, "/api/v1/credentials/"+created.Data.ID, token, "", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"hasSecret":true`) {
		t.Fatalf("detail = %d: %s", detail.Code, detail.Body.String())
	}
	list := apiJSON(t, handler, http.MethodGet, "/api/v1/credentials", token, "", nil)
	var page pageResponse
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 1 || page.Data.Items[0]["hasSecret"] != true {
		t.Fatalf("list items = %+v", page.Data.Items)
	}
	if _, present := page.Data.Items[0]["secretCiphertext"]; present {
		t.Fatalf("list leaked the ciphertext column: %+v", page.Data.Items[0])
	}
	if strings.Contains(list.Body.String(), "s3cr3t-password") {
		t.Fatal("list response contains the plaintext password")
	}

	rotated := apiJSON(t, handler, http.MethodPatch, "/api/v1/credentials/"+created.Data.ID, token, "", map[string]any{
		"secret": map[string]any{"password": "rotated-password"},
	})
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate status = %d: %s", rotated.Code, rotated.Body.String())
	}
	if strings.Contains(rotated.Body.String(), "rotated-password") || strings.Contains(rotated.Body.String(), "s3cr3t-password") {
		t.Fatalf("rotate response echoed a secret: %s", rotated.Body.String())
	}
	_, secret, err := api.credentialService.GetWithSecret(context.Background(), auth.Principal{UserID: user.ID, Role: "user"}, created.Data.ID)
	if err != nil || secret == nil || secret.Password != "rotated-password" {
		t.Fatalf("stored secret = %+v, err = %v", secret, err)
	}
}

func TestCredentialAPIStoresPrivateKeySecret(t *testing.T) {
	api, _, user := apiTestServer(t)
	setCredentialSecretStore(t, api)
	token := apiToken(t, api, user.Username, "alice-pass")
	handler := api.Handler()
	privateKey, publicKey := testEd25519PrivateKeyPEM(t, "key-pass")

	create := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials", token, "cred-key", map[string]any{
		"name": "deploy", "type": "ssh_public_key", "publicKey": publicKey, "enabled": true,
		"secret": map[string]any{"privateKey": privateKey, "passphrase": "key-pass"},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "PRIVATE KEY") || strings.Contains(create.Body.String(), "key-pass") {
		t.Fatalf("create response leaked private key material: %s", create.Body.String())
	}
	var created envelope
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	_, secret, err := api.credentialService.GetWithSecret(context.Background(), auth.Principal{UserID: user.ID, Role: "user"}, created.Data.ID)
	if err != nil || secret == nil || secret.PrivateKey != privateKey || secret.Passphrase != "key-pass" {
		t.Fatalf("stored secret = %+v, err = %v", secret, err)
	}
}

func TestCredentialAPIValidatesSecretShape(t *testing.T) {
	api, _, user := apiTestServer(t)
	setCredentialSecretStore(t, api)
	token := apiToken(t, api, user.Username, "alice-pass")
	handler := api.Handler()

	cases := []struct {
		name string
		body map[string]any
	}{
		{name: "password-without-secret", body: map[string]any{"name": "a", "type": "password", "enabled": true}},
		{name: "password-with-private-key", body: map[string]any{"name": "a", "type": "password", "enabled": true, "secret": map[string]any{"privateKey": "key"}}},
		{name: "public-key-with-password-secret", body: map[string]any{"name": "a", "type": "ssh_public_key", "publicKey": validPublicKey, "enabled": true, "secret": map[string]any{"password": "p"}}},
		{name: "oversized-password", body: map[string]any{"name": "a", "type": "password", "enabled": true, "secret": map[string]any{"password": strings.Repeat("x", 4097)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials", token, "", tc.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			// A schema rejection would also be 400, so require the service-level
			// credential message to prove validation ran.
			if !strings.Contains(response.Body.String(), "invalid ssh credential") {
				t.Fatalf("body = %s, want a credential validation error", response.Body.String())
			}
		})
	}
}

func TestCredentialAPIRejectsSecretWithoutEncryptionKey(t *testing.T) {
	api, _, user := apiTestServer(t)
	api.credentialService.SetSecretStore(nil)
	token := apiToken(t, api, user.Username, "alice-pass")
	handler := api.Handler()

	response := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials", token, "", map[string]any{
		"name": "web-host", "type": "password", "enabled": true,
		"secret": map[string]any{"password": "s3cr3t"},
	})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "s3cr3t") {
		t.Fatalf("error response echoed the secret: %s", response.Body.String())
	}
	// A credential without a secret still works when no key is configured.
	if r := apiJSON(t, handler, http.MethodPost, "/api/v1/credentials", token, "", map[string]any{
		"name": "public-only", "type": "ssh_public_key", "publicKey": validPublicKey, "enabled": true,
	}); r.Code != http.StatusCreated {
		t.Fatalf("public-only create status = %d: %s", r.Code, r.Body.String())
	}
}
