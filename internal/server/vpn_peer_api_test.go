package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// vpnAPINodePublicKey is the RFC 7748 base-point public key used across the vpn
// tests. It is a published test vector, not a secret.
const vpnAPINodePublicKey = "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="

// vpnAPIFixture is an authenticated management API with the peer service
// assembled over the same SQLite database the other API tests use, plus a second
// non-admin account so ownership can be exercised from the HTTP layer.
type vpnAPIFixture struct {
	api        *API
	handler    http.Handler
	userToken  string
	otherToken string
	adminToken string
	userAgent  string
	otherAgent string
	otherID    string
}

func newVPNAPIFixture(t *testing.T, mutate func(*VPNServiceConfig)) *vpnAPIFixture {
	t.Helper()
	api, admin, user := apiTestServer(t)
	ctx := context.Background()
	accounts := auth.NewAuthService(api.DB)
	other, err := accounts.CreateUser(ctx, "bob", "bob-pass", "user")
	if err != nil {
		t.Fatalf("create the second user: %v", err)
	}
	for _, agent := range []storage.Agent{
		{ID: "vpn-agent-alice", Name: "alice egress", OwnerUserID: user.ID, Enabled: true},
		{ID: "vpn-agent-bob", Name: "bob egress", OwnerUserID: other.ID, Enabled: true},
	} {
		if err := api.DB.Agents().Create(ctx, agent); err != nil {
			t.Fatalf("create agent %s: %v", agent.ID, err)
		}
	}
	// The store is injected rather than read from the environment, so the tests
	// neither depend on nor disturb TUNNELMESH_TOKEN_ENCRYPTION_KEY.
	store, err := auth.NewSecretStore(base64.RawStdEncoding.EncodeToString(make([]byte, 32)), "vpn-api-test-key")
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	cfg := VPNServiceConfig{
		Enabled: true, IPPool: "10.64.0.0/16", NodeSubnetSize: 24,
		EndpointHost: "gw-1.mesh.example.com", Listen: "0.0.0.0:51820", MTU: 1420,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	api.SetVPNPeerService(NewVPNPeerService(VPNPeerServiceDeps{
		Peers: api.DB.VPNPeers(), Leases: api.DB.VPNIPLeases(),
		Agents: api.DB.Agents(), Audits: api.DB.Audits(),
		Secrets: store, Config: cfg, NodeID: "api-node-1", LeaseTTL: time.Minute,
		NodePublicKey: vpnAPINodePublicKey,
	}))
	return &vpnAPIFixture{
		api: api, handler: api.Handler(),
		userToken:  apiToken(t, api, "alice", "alice-pass"),
		otherToken: apiToken(t, api, "bob", "bob-pass"),
		adminToken: apiToken(t, api, admin.Username, "admin-pass"),
		userAgent:  "vpn-agent-alice",
		otherAgent: "vpn-agent-bob",
		otherID:    other.ID,
	}
}

type vpnAPIEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type vpnPeerPayload struct {
	ID               string   `json:"id"`
	OwnerUserID      string   `json:"ownerUserId"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	PublicKey        string   `json:"publicKey"`
	VPNIP            string   `json:"vpnIp"`
	NodeID           string   `json:"nodeId"`
	AgentID          string   `json:"agentId"`
	AllowedIPs       []string `json:"allowedIps"`
	AllowedPorts     []int    `json:"allowedPorts"`
	Status           string   `json:"status"`
	ConfigRevealPath string   `json:"configRevealPath"`
	PrivateKey       string   `json:"privateKey"`
	Ciphertext       string   `json:"privateKeyCiphertext"`
	Nonce            string   `json:"privateKeyNonce"`
	KeyID            string   `json:"privateKeyKeyId"`
}

type vpnPagePayload struct {
	Items      []vpnPeerPayload `json:"items"`
	NextCursor string           `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
}

type vpnRevealPayload struct {
	ID     string `json:"id"`
	VPNIP  string `json:"vpnIp"`
	Format string `json:"format"`
	Config string `json:"config"`
}

func vpnDecodeEnvelope(t *testing.T, r *httptest.ResponseRecorder) vpnAPIEnvelope {
	t.Helper()
	var envelope vpnAPIEnvelope
	if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the envelope: %v (%s)", err, r.Body.String())
	}
	return envelope
}

// vpnDataError reads data.error, which carries the stable code for a mapped vpn
// failure and prose for a transport-level one.
func vpnDataError(t *testing.T, r *httptest.ResponseRecorder) string {
	t.Helper()
	envelope := vpnDecodeEnvelope(t, r)
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		t.Fatalf("decode the error payload: %v (%s)", err, envelope.Data)
	}
	return payload.Error
}

func vpnAssertError(t *testing.T, r *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status = %d, want %d (%s)", r.Code, status, r.Body.String())
	}
	if got := vpnDataError(t, r); got != code {
		t.Fatalf("data.error = %q, want %q (%s)", got, code, r.Body.String())
	}
}

func vpnDecodePeer(t *testing.T, r *httptest.ResponseRecorder) vpnPeerPayload {
	t.Helper()
	envelope := vpnDecodeEnvelope(t, r)
	var peer vpnPeerPayload
	if err := json.Unmarshal(envelope.Data, &peer); err != nil {
		t.Fatalf("decode the peer: %v (%s)", err, envelope.Data)
	}
	return peer
}

func vpnDecodePage(t *testing.T, r *httptest.ResponseRecorder) vpnPagePayload {
	t.Helper()
	envelope := vpnDecodeEnvelope(t, r)
	var page vpnPagePayload
	if err := json.Unmarshal(envelope.Data, &page); err != nil {
		t.Fatalf("decode the page: %v (%s)", err, envelope.Data)
	}
	return page
}

func vpnDecodeReveal(t *testing.T, r *httptest.ResponseRecorder) vpnRevealPayload {
	t.Helper()
	envelope := vpnDecodeEnvelope(t, r)
	var revealed vpnRevealPayload
	if err := json.Unmarshal(envelope.Data, &revealed); err != nil {
		t.Fatalf("decode the revealed configuration: %v (%s)", err, envelope.Data)
	}
	return revealed
}

func vpnPeerBody(agentID, name string) map[string]any {
	return map[string]any{
		"name": name, "description": "api test peer", "agentId": agentID,
		"allowedIps": []string{"10.0.0.0/8"}, "allowedPorts": []int{443},
		"maxConcurrentFlows": 64,
	}
}

func (f *vpnAPIFixture) createPeer(t *testing.T, token, agentID, name string) vpnPeerPayload {
	t.Helper()
	r := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", token, "", vpnPeerBody(agentID, name))
	if r.Code != http.StatusCreated {
		t.Fatalf("create %s: status = %d (%s)", name, r.Code, r.Body.String())
	}
	return vpnDecodePeer(t, r)
}

// TestVPNPeerAPIRequiresAuthentication covers every route in the surface. The
// bearer check runs in ServeHTTP before dispatch, so an unauthenticated caller
// learns nothing about which vpn paths exist.
func TestVPNPeerAPIRequiresAuthentication(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	created := f.createPeer(t, f.userToken, f.userAgent, "auth-probe")
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/vpn-peers"},
		{http.MethodPost, "/api/v1/vpn-peers"},
		{http.MethodGet, "/api/v1/vpn-peers/" + created.ID},
		{http.MethodPatch, "/api/v1/vpn-peers/" + created.ID},
		{http.MethodDelete, "/api/v1/vpn-peers/" + created.ID},
		{http.MethodPost, "/api/v1/vpn-peers/" + created.ID + "/rotate"},
		{http.MethodPost, "/api/v1/vpn-peers/" + created.ID + "/config:reveal"},
		{http.MethodGet, "/api/v1/vpn-peers/" + created.ID + "/flows"},
		{http.MethodGet, "/api/v1/vpn-nodes"},
	} {
		r := apiJSON(t, f.handler, call.method, call.path, "", "", nil)
		if r.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", call.method, call.path, r.Code)
		}
	}
}

// TestVPNPeerAPIUnavailableWithoutAssembly pins the failure mode of a server
// whose runtime never installed the service: 503 rather than a 404 that would
// read as "this product has no vpn api".
func TestVPNPeerAPIUnavailableWithoutAssembly(t *testing.T) {
	api, _, user := apiTestServer(t)
	token := apiToken(t, api, user.Username, "alice-pass")
	r := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/vpn-peers", token, "", nil)
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", r.Code, r.Body.String())
	}
}

// TestVPNPeerAPISetVPNAssemblesTheService covers the configuration-driven
// assembly the runtime uses, so a wiring mistake cannot hide behind the
// hand-assembled fixture every other test installs.
func TestVPNPeerAPISetVPNAssemblesTheService(t *testing.T) {
	api, _, user := apiTestServer(t)
	api.SetVPN(config.VPNConfig{
		Enabled: true, IPPool: "10.64.0.0/16", NodeSubnetSize: 24,
		EndpointHost: "gw-1.mesh.example.com", Listen: "0.0.0.0:51820", MTU: 1420,
	}, "node-from-config")
	token := apiToken(t, api, user.Username, "alice-pass")
	r := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/vpn-peers", token, "", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", r.Code, r.Body.String())
	}
	if page := vpnDecodePage(t, r); len(page.Items) != 0 {
		t.Fatalf("a fresh node listed %d peers", len(page.Items))
	}
}

func TestVPNPeerAPICreateAndRead(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	r := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", vpnPeerBody(f.userAgent, "laptop"))
	if r.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", r.Code, r.Body.String())
	}
	created := vpnDecodePeer(t, r)
	if created.ID == "" || created.VPNIP == "" || len(created.PublicKey) != 44 {
		t.Fatalf("created summary = %+v", created)
	}
	if created.NodeID != "api-node-1" || created.AgentID != f.userAgent || created.Status != "active" {
		t.Fatalf("created summary = %+v", created)
	}
	if len(created.AllowedIPs) != 1 || created.AllowedIPs[0] != "10.0.0.0/8" {
		t.Fatalf("allowedIps = %v, want the parsed policy", created.AllowedIPs)
	}
	if len(created.AllowedPorts) != 1 || created.AllowedPorts[0] != 443 {
		t.Fatalf("allowedPorts = %v", created.AllowedPorts)
	}
	if created.ConfigRevealPath != "/api/v1/vpn-peers/"+created.ID+"/config:reveal" {
		t.Fatalf("configRevealPath = %q", created.ConfigRevealPath)
	}
	// No summary may carry key material, sealed or not: the ciphertext columns
	// are internal state and the plaintext exists only in a reveal response.
	body := r.Body.String()
	for _, forbidden := range []string{"privateKey", "iphertext", "once", "eyId"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the create response mentions %q: %s", forbidden, body)
		}
	}
	if created.PrivateKey != "" || created.Ciphertext != "" || created.Nonce != "" || created.KeyID != "" {
		t.Fatalf("the create response leaked key material: %+v", created)
	}

	detail := apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers/"+created.ID, f.userToken, "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d (%s)", detail.Code, detail.Body.String())
	}
	if got := vpnDecodePeer(t, detail); got.ID != created.ID || got.VPNIP != created.VPNIP {
		t.Fatalf("detail = %+v, want the created peer", got)
	}
	// An administrator operates the whole fleet and may read any peer.
	if r := apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers/"+created.ID, f.adminToken, "", nil); r.Code != http.StatusOK {
		t.Fatalf("admin detail status = %d (%s)", r.Code, r.Body.String())
	}
	if r := apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers/vpn-peer-missing", f.userToken, "", nil); r.Code != http.StatusNotFound {
		t.Fatalf("missing peer status = %d", r.Code)
	}
}

// TestVPNPeerAPICreateIsIdempotent is the guarantee that a retried POST cannot
// burn a second address: the replayed response is byte-identical and the node
// still holds exactly one peer.
func TestVPNPeerAPICreateIsIdempotent(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	body := vpnPeerBody(f.userAgent, "laptop")
	first := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "vpn-key-1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", first.Code, first.Body.String())
	}
	replay := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "vpn-key-1", body)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%s, want the identical stored response", replay.Code, replay.Body.String())
	}
	page := vpnDecodePage(t, apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers", f.userToken, "", nil))
	if len(page.Items) != 1 {
		t.Fatalf("the replay created %d peers, want 1", len(page.Items))
	}
	// A key claimed by somebody else is a conflict, not a silent replay.
	if r := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.otherToken, "vpn-key-1", vpnPeerBody(f.otherAgent, "other")); r.Code != http.StatusConflict {
		t.Fatalf("a stolen idempotency key returned %d (%s)", r.Code, r.Body.String())
	}
}

func TestVPNPeerAPIPatchRotateAndRevoke(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	created := f.createPeer(t, f.userToken, f.userAgent, "laptop")
	path := "/api/v1/vpn-peers/" + created.ID

	patched := apiJSON(t, f.handler, http.MethodPatch, path, f.userToken, "", map[string]any{
		"name": "renamed", "allowedIps": []string{"192.0.2.0/24"}, "allowedPorts": []int{8443},
	})
	if patched.Code != http.StatusOK {
		t.Fatalf("patch status = %d (%s)", patched.Code, patched.Body.String())
	}
	updated := vpnDecodePeer(t, patched)
	if updated.Name != "renamed" || updated.AllowedIPs[0] != "192.0.2.0/24" || updated.AllowedPorts[0] != 8443 {
		t.Fatalf("patched peer = %+v", updated)
	}
	if updated.VPNIP != created.VPNIP || updated.PublicKey != created.PublicKey {
		t.Fatal("the patch changed a field it was not given")
	}
	// An empty patch is a caller mistake, not a no-op: answering 200 would hide
	// a console bug that silently sends nothing.
	if r := apiJSON(t, f.handler, http.MethodPatch, path, f.userToken, "", map[string]any{}); r.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d (%s)", r.Code, r.Body.String())
	}
	// PATCH cannot reach the terminal state; revocation has its own endpoint.
	r := apiJSON(t, f.handler, http.MethodPatch, path, f.userToken, "", map[string]any{"status": "revoked"})
	vpnAssertError(t, r, http.StatusBadRequest, "vpn_peer_invalid")
	if r := apiJSON(t, f.handler, http.MethodPatch, path, f.userToken, "", map[string]any{"status": "disabled"}); r.Code != http.StatusOK {
		t.Fatalf("disabling through PATCH returned %d (%s)", r.Code, r.Body.String())
	}

	rotated := apiJSON(t, f.handler, http.MethodPost, path+"/rotate", f.userToken, "vpn-rotate-1", nil)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate status = %d (%s)", rotated.Code, rotated.Body.String())
	}
	if got := vpnDecodePeer(t, rotated); got.PublicKey == created.PublicKey || got.VPNIP != created.VPNIP {
		t.Fatalf("rotated peer = %+v", got)
	}
	// A retried rotation must not rotate again: the user has already downloaded
	// the configuration the first response described.
	replay := apiJSON(t, f.handler, http.MethodPost, path+"/rotate", f.userToken, "vpn-rotate-1", nil)
	if replay.Code != http.StatusOK || replay.Body.String() != rotated.Body.String() {
		t.Fatalf("rotate replay = %d/%s", replay.Code, replay.Body.String())
	}

	revoked := apiJSON(t, f.handler, http.MethodDelete, path, f.userToken, "", nil)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke status = %d (%s)", revoked.Code, revoked.Body.String())
	}
	if got := vpnDecodePeer(t, revoked); got.Status != "revoked" {
		t.Fatalf("revoked status = %q", got.Status)
	}
	// The row stays readable for audit, and both mutations are now refused.
	if r := apiJSON(t, f.handler, http.MethodGet, path, f.userToken, "", nil); r.Code != http.StatusOK {
		t.Fatalf("reading a revoked peer returned %d", r.Code)
	}
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPost, path+"/rotate", f.userToken, "", nil), http.StatusConflict, "vpn_peer_conflict")
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPatch, path, f.userToken, "", map[string]any{"name": "x"}), http.StatusConflict, "vpn_peer_conflict")
	// Revoking twice is not something the caller can act on.
	if r := apiJSON(t, f.handler, http.MethodDelete, path, f.userToken, "", nil); r.Code != http.StatusOK {
		t.Fatalf("a second revoke returned %d (%s)", r.Code, r.Body.String())
	}
}

// TestVPNPeerAPIPaginationKeepsOwnersApart is D11 verified through HTTP: the
// owner restriction is applied inside the query, so another user's peer appears
// on no page, not even a later one.
func TestVPNPeerAPIPaginationKeepsOwnersApart(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	for i := 0; i < 3; i++ {
		f.createPeer(t, f.userToken, f.userAgent, "alice-peer")
		f.createPeer(t, f.otherToken, f.otherAgent, "bob-peer")
	}
	cursor := ""
	seen, pages := 0, 0
	for {
		path := "/api/v1/vpn-peers?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		r := apiJSON(t, f.handler, http.MethodGet, path, f.userToken, "", nil)
		if r.Code != http.StatusOK {
			t.Fatalf("page %d status = %d (%s)", pages, r.Code, r.Body.String())
		}
		page := vpnDecodePage(t, r)
		for _, peer := range page.Items {
			if peer.Name != "alice-peer" {
				t.Fatalf("page %d leaked %s owned by %s", pages, peer.Name, peer.OwnerUserID)
			}
		}
		seen += len(page.Items)
		pages++
		if !page.HasMore || pages > 10 {
			break
		}
		cursor = page.NextCursor
	}
	if seen != 3 || pages != 2 {
		t.Fatalf("alice saw %d peers over %d pages, want 3 over 2", seen, pages)
	}
	admin := vpnDecodePage(t, apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers?limit=50", f.adminToken, "", nil))
	if len(admin.Items) != 6 {
		t.Fatalf("the admin saw %d peers, want 6", len(admin.Items))
	}
	// A user may not widen their own view by asking for somebody else's rows:
	// the owner filter is set from the authenticated principal, never from the
	// query string.
	filtered := vpnDecodePage(t, apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers?owner="+f.otherID, f.userToken, "", nil))
	if len(filtered.Items) != 3 {
		t.Fatalf("an owner query parameter widened the view to %d peers", len(filtered.Items))
	}
}

// TestVPNPeerAPICrossUserAccessIsNotFound asserts the anti-enumeration rule from
// the HTTP layer: every verb on somebody else's peer answers 404, never 403,
// because 403 confirms the identifier exists.
func TestVPNPeerAPICrossUserAccessIsNotFound(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	created := f.createPeer(t, f.userToken, f.userAgent, "alice-secret")
	path := "/api/v1/vpn-peers/" + created.ID
	for _, call := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, path, nil},
		{http.MethodPatch, path, map[string]any{"name": "stolen"}},
		{http.MethodDelete, path, nil},
		{http.MethodPost, path + "/rotate", nil},
		{http.MethodPost, path + "/config:reveal", map[string]any{"acknowledgeRisk": true}},
		{http.MethodGet, path + "/flows", nil},
	} {
		r := apiJSONWithHeaders(t, f.handler, call.method, call.path, f.otherToken, map[string]string{
			"X-VPN-Config-Reveal-Confirm": "yes", "Idempotency-Key": "cross-user-" + call.method,
		}, call.body)
		if r.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404 (%s)", call.method, call.path, r.Code, r.Body.String())
			continue
		}
		if strings.Contains(strings.ToLower(r.Body.String()), "forbidden") {
			t.Errorf("%s %s leaked a forbidden response: %s", call.method, call.path, r.Body.String())
		}
	}
	// Attaching a peer to somebody else's egress agent is refused with the same
	// answer as a missing agent, so agent IDs cannot be enumerated either.
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", vpnPeerBody(f.otherAgent, "through-others")),
		http.StatusBadRequest, "vpn_peer_invalid")
	// An administrator may use any agent, because operating the fleet is the job.
	if r := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.adminToken, "", vpnPeerBody(f.otherAgent, "admin-peer")); r.Code != http.StatusCreated {
		t.Fatalf("admin create status = %d (%s)", r.Code, r.Body.String())
	}
}

// TestVPNPeerAPIRevealGuardsThePrivateKey walks the three preconditions of the
// approved reveal pattern and then the property that matters most: the response
// is never cached and never written into the idempotency store, because that
// store persists response bodies in the clear.
func TestVPNPeerAPIRevealGuardsThePrivateKey(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	created := f.createPeer(t, f.userToken, f.userAgent, "reveal-me")
	path := "/api/v1/vpn-peers/" + created.ID + "/config:reveal"

	for name, headers := range map[string]map[string]string{
		"no confirmation header": {"Idempotency-Key": "vpn-reveal-1"},
		"no idempotency key":     {"X-VPN-Config-Reveal-Confirm": "yes"},
	} {
		r := apiJSONWithHeaders(t, f.handler, http.MethodPost, path, f.userToken, headers, map[string]any{"acknowledgeRisk": true})
		if r.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, r.Code, r.Body.String())
		}
	}
	r := apiJSONWithHeaders(t, f.handler, http.MethodPost, path, f.userToken, map[string]string{
		"X-VPN-Config-Reveal-Confirm": "yes", "Idempotency-Key": "vpn-reveal-1",
	}, map[string]any{"acknowledgeRisk": false})
	if r.Code != http.StatusBadRequest {
		t.Fatalf("acknowledgeRisk=false returned %d (%s)", r.Code, r.Body.String())
	}
	// An unknown field is rejected rather than ignored, so a misspelled
	// acknowledgement cannot look like a confirmed one.
	r = apiJSONWithHeaders(t, f.handler, http.MethodPost, path, f.userToken, map[string]string{
		"X-VPN-Config-Reveal-Confirm": "yes", "Idempotency-Key": "vpn-reveal-2",
	}, map[string]any{"acknowledgeRisk": true, "reason": "audit"})
	if r.Code != http.StatusBadRequest {
		t.Fatalf("an unknown reveal field returned %d (%s)", r.Code, r.Body.String())
	}

	ok := apiJSONWithHeaders(t, f.handler, http.MethodPost, path, f.userToken, map[string]string{
		"X-VPN-Config-Reveal-Confirm": "yes", "Idempotency-Key": "vpn-reveal-3",
	}, map[string]any{"acknowledgeRisk": true})
	if ok.Code != http.StatusOK {
		t.Fatalf("reveal status = %d (%s)", ok.Code, ok.Body.String())
	}
	if got := ok.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	revealed := vpnDecodeReveal(t, ok)
	if revealed.ID != created.ID || revealed.Format != "wg-quick" {
		t.Fatalf("reveal payload = %+v", revealed)
	}
	for _, want := range []string{"[Interface]", "[Peer]", "PrivateKey = ", "Address = " + created.VPNIP + "/32", "PublicKey = " + vpnAPINodePublicKey} {
		if !strings.Contains(revealed.Config, want) {
			t.Errorf("the rendered configuration is missing %q:\n%s", want, revealed.Config)
		}
	}
	// The idempotency store persists response bodies, so a reveal must never go
	// through it: that would write a private key into the database in the clear.
	if _, err := f.api.DB.Idempotency().Get(context.Background(), "vpn-reveal-3"); err == nil {
		t.Fatal("the reveal response was persisted in the idempotency store")
	}
	// A second confirmed reveal works and returns the same key, because the key
	// is sealed rather than hashed: it is recoverable by design.
	again := apiJSONWithHeaders(t, f.handler, http.MethodPost, path, f.userToken, map[string]string{
		"X-VPN-Config-Reveal-Confirm": "yes", "Idempotency-Key": "vpn-reveal-4",
	}, map[string]any{"acknowledgeRisk": true})
	if again.Code != http.StatusOK {
		t.Fatalf("second reveal status = %d (%s)", again.Code, again.Body.String())
	}
	if vpnDecodeReveal(t, again).Config != revealed.Config {
		t.Fatal("two reveals of an unrotated peer returned different configurations")
	}
	// The reveal is audited, and the audit trail carries no key material.
	page, err := f.api.DB.Audits().List(context.Background(), storage.AuditFilter{Action: vpnAuditRevealed}, "", 50)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("found %d reveal audit entries, want 2", len(page.Items))
	}
	for _, entry := range page.Items {
		if entry.ResourceID != created.ID {
			t.Errorf("the reveal audit entry points at %q", entry.ResourceID)
		}
		if strings.Contains(entry.Details, "PrivateKey") {
			t.Errorf("the reveal audit entry carries key material: %s", entry.Details)
		}
	}
}

// TestVPNPeerAPIRefusesTheDataPlaneEndpoints pins D12: an endpoint whose data
// plane dependency has not shipped answers 501 with the stable code, never a 200
// with an empty list that a console would render as "nothing is happening".
func TestVPNPeerAPIRefusesTheDataPlaneEndpoints(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	created := f.createPeer(t, f.userToken, f.userAgent, "flows")
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers/"+created.ID+"/flows", f.userToken, "", nil),
		http.StatusNotImplemented, "vpn_not_implemented")
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-nodes", f.adminToken, "", nil),
		http.StatusNotImplemented, "vpn_not_implemented")
	// A missing peer is still a 404: the flows route resolves the peer before
	// reporting what it cannot do, so it cannot be used to probe the build.
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers/vpn-peer-missing/flows", f.userToken, "", nil),
		http.StatusNotFound, "vpn_peer_not_found")
}

func TestVPNPeerAPIDisabledNode(t *testing.T) {
	f := newVPNAPIFixture(t, func(c *VPNServiceConfig) { c.Enabled = false })
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", vpnPeerBody(f.userAgent, "nope")),
		http.StatusConflict, "vpn_node_disabled")
	// Reads stay available and report nothing, so a console wired up ahead of
	// the gateway renders an empty table instead of an error page.
	r := apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers", f.userToken, "", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s)", r.Code, r.Body.String())
	}
	if page := vpnDecodePage(t, r); len(page.Items) != 0 {
		t.Fatalf("a disabled node listed %d peers", len(page.Items))
	}
}

// TestVPNPeerAPIRejectsMalformedRequests covers the transport-level rules: the
// method allowlist, unknown sub-resources, unknown body fields, and the two
// request errors a caller can make that must be answered before anything is
// allocated.
func TestVPNPeerAPIRejectsMalformedRequests(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	created := f.createPeer(t, f.userToken, f.userAgent, "malformed")
	path := "/api/v1/vpn-peers/" + created.ID

	if r := apiJSON(t, f.handler, http.MethodPost, path, f.userToken, "", nil); r.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST on a peer returned %d, want 405", r.Code)
	}
	if r := apiJSON(t, f.handler, http.MethodPut, path, f.userToken, "", nil); r.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT on a peer returned %d, want 405", r.Code)
	}
	if r := apiJSON(t, f.handler, http.MethodGet, path+"/bogus", f.userToken, "", nil); r.Code != http.StatusNotFound {
		t.Errorf("an unknown action returned %d, want 404", r.Code)
	}
	if r := apiJSON(t, f.handler, http.MethodGet, path+"/rotate", f.userToken, "", nil); r.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on rotate returned %d, want 405", r.Code)
	}
	if r := apiJSON(t, f.handler, http.MethodGet, "/api/v1/vpn-peers/"+created.ID+"/x/y", f.userToken, "", nil); r.Code != http.StatusNotFound {
		t.Errorf("a deep path returned %d, want 404", r.Code)
	}
	if r := apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", map[string]any{"name": "x", "bogus": true}); r.Code != http.StatusBadRequest {
		t.Errorf("an unknown create field returned %d, want 400", r.Code)
	}
	body := vpnPeerBody(f.userAgent, "wants-icmp")
	body["icmpEnabled"] = true
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", body),
		http.StatusConflict, "vpn_agent_capability_missing")
	badCIDR := vpnPeerBody(f.userAgent, "bad-cidr")
	badCIDR["allowedIps"] = []string{"10.0.0.1"}
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", badCIDR),
		http.StatusBadRequest, "vpn_peer_invalid")
	// An unusable pool is an operator fault that the first issue reports, since
	// nothing at load time can know a peer will ever be requested.
	store, err := auth.NewSecretStore(base64.RawStdEncoding.EncodeToString(make([]byte, 32)), "vpn-api-test-key")
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	f.api.SetVPNPeerService(NewVPNPeerService(VPNPeerServiceDeps{
		Peers: f.api.DB.VPNPeers(), Leases: f.api.DB.VPNIPLeases(),
		Agents: f.api.DB.Agents(), Audits: f.api.DB.Audits(),
		Secrets: store,
		Config:  VPNServiceConfig{Enabled: true, IPPool: "10.64.0.0/33", NodeSubnetSize: 24},
		NodeID:  "api-node-1",
	}))
	vpnAssertError(t, apiJSON(t, f.handler, http.MethodPost, "/api/v1/vpn-peers", f.userToken, "", vpnPeerBody(f.userAgent, "no-pool")),
		http.StatusBadRequest, "vpn_ip_pool_invalid")
}
