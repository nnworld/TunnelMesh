package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// createProxyTestAgent registers one Agent and returns its ID; every tp-* route
// needs an egress Agent to point at.
func createProxyTestAgent(t *testing.T, h http.Handler, token, name string) string {
	t.Helper()
	created := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "proxy-agent-"+name, map[string]any{"name": name})
	if created.Code != http.StatusCreated {
		t.Fatalf("agent create status=%d: %s", created.Code, created.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	return agent.Data.ID
}

// createProxyTestCredential registers one enabled proxy_basic credential and
// returns its ID. The password is write-only, so only the ID is asserted here.
func createProxyTestCredential(t *testing.T, h http.Handler, token, name, username, password string) string {
	t.Helper()
	created := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "proxy-cred-"+name, map[string]any{
		"name": name, "type": "proxy_basic", "username": username, "enabled": true,
		"secret": map[string]any{"password": password},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("credential create status=%d: %s", created.Code, created.Body.String())
	}
	var cred struct {
		Data struct {
			ID        string `json:"id"`
			Username  string `json:"username"`
			HasSecret bool   `json:"hasSecret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &cred); err != nil {
		t.Fatal(err)
	}
	if cred.Data.Username != username || !cred.Data.HasSecret {
		t.Fatalf("credential response = %+v", cred.Data)
	}
	return cred.Data.ID
}

// proxyRouteResponse is the subset of the route envelope these tests assert on.
// The proxy policy fields are rendered flat next to the routing fields, which is
// what lets the admin UI show one tp-* route without decoding config JSON.
type proxyRouteResponse struct {
	Data struct {
		ID                   string   `json:"id"`
		Protocol             string   `json:"protocol"`
		Domain               string   `json:"domain"`
		PathPrefix           string   `json:"pathPrefix"`
		TargetHost           string   `json:"targetHost"`
		TargetPort           int      `json:"targetPort"`
		ProxyURL             string   `json:"proxyUrl"`
		AuthMode             string   `json:"authMode"`
		CredentialID         string   `json:"credentialId"`
		SourceCIDRs          []string `json:"sourceCIDRs"`
		TargetCIDRs          []string `json:"targetCIDRs"`
		TargetPorts          []int    `json:"targetPorts"`
		AllowPrivateTargets  bool     `json:"allowPrivateTargets"`
		MaxConcurrentTunnels int      `json:"maxConcurrentTunnels"`
		Description          string   `json:"description"`
		Status               string   `json:"status"`
	} `json:"data"`
}

func TestAPICreatesProxyRouteWithSentinelTarget(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	agentID := createProxyTestAgent(t, h, token, "egress")
	credentialID := createProxyTestCredential(t, h, token, "proxy demo", "demo", "s3cret")

	created := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-route-1", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-demo.tm.example.com",
		"authMode": "basic", "credentialId": credentialID,
		"sourceCIDRs": []string{"11.71.85.0/24", "10.1.2.3"},
		"targetCIDRs": []string{"10.10.0.0/16"}, "targetPorts": []int{443, 443, 8443},
		"allowPrivateTargets": true, "maxConcurrentTunnels": 8, "description": "demo egress",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d: %s", created.Code, created.Body.String())
	}
	var got proxyRouteResponse
	if err := json.Unmarshal(created.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	d := got.Data
	if d.Protocol != "http-proxy" || d.TargetHost != "*" || d.TargetPort != 0 || d.PathPrefix != "/" {
		t.Fatalf("sentinel target not enforced: %+v", d)
	}
	if d.ProxyURL != "https://tp-demo.tm.example.com" {
		t.Fatalf("proxyUrl = %q", d.ProxyURL)
	}
	if d.AuthMode != "basic" || d.CredentialID != credentialID || d.Description != "demo egress" {
		t.Fatalf("policy fields = %+v", d)
	}
	if len(d.SourceCIDRs) != 2 || d.SourceCIDRs[1] != "10.1.2.3/32" {
		t.Fatalf("bare IP was not normalized to a CIDR: %#v", d.SourceCIDRs)
	}
	if len(d.TargetCIDRs) != 1 || d.TargetCIDRs[0] != "10.10.0.0/16" {
		t.Fatalf("targetCIDRs = %#v", d.TargetCIDRs)
	}
	if len(d.TargetPorts) != 2 || d.TargetPorts[0] != 443 || d.TargetPorts[1] != 8443 {
		t.Fatalf("target ports not deduped and sorted: %#v", d.TargetPorts)
	}
	if !d.AllowPrivateTargets || d.MaxConcurrentTunnels != 8 || d.Status != "active" {
		t.Fatalf("policy switches = %+v", d)
	}
}

// A tp-* domain is one whole-host proxy endpoint, so exactly one route may claim
// it. This locks three facets of that rule: a case variant counts as the same
// claim, a path-level reverse-proxy route may not share the hostname, and a
// PATCH that renames a route onto a claimed domain is refused too.
func TestAPIProxyRouteRejectsDuplicateDomain(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	agentID := createProxyTestAgent(t, h, token, "egress-dup")

	first := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-1", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-dup.tm.example.com",
		"authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("create status=%d: %s", first.Code, first.Body.String())
	}
	var created proxyRouteResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	upper := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-2", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "TP-DUP.tm.example.com",
		"authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
	})
	if upper.Code != http.StatusConflict {
		t.Fatalf("case-variant duplicate status=%d body=%s, want 409", upper.Code, upper.Body.String())
	}

	reverse := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-3", map[string]any{
		"agentId": agentID, "protocol": "http", "domain": "tp-dup.tm.example.com", "pathPrefix": "/admin",
		"targetHost": "10.0.0.9", "targetPort": 8080,
	})
	if reverse.Code != http.StatusConflict {
		t.Fatalf("reverse-proxy route on a tp-* domain status=%d body=%s, want 409", reverse.Code, reverse.Body.String())
	}

	other := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-4", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-other.tm.example.com",
		"authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
	})
	if other.Code != http.StatusCreated {
		t.Fatalf("second proxy route status=%d: %s", other.Code, other.Body.String())
	}
	var otherRoute proxyRouteResponse
	if err := json.Unmarshal(other.Body.Bytes(), &otherRoute); err != nil {
		t.Fatal(err)
	}
	if otherRoute.Data.ID == created.Data.ID {
		t.Fatal("two distinct proxy routes share an ID")
	}

	renamed := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+otherRoute.Data.ID, token, "", map[string]any{
		"domain": "tp-dup.tm.example.com",
	})
	if renamed.Code != http.StatusConflict {
		t.Fatalf("rename onto an existing tp-* domain status=%d body=%s, want 409", renamed.Code, renamed.Body.String())
	}
}

// The reverse order must be refused as well: when a reverse-proxy route already
// occupies a tp-* hostname, a later proxy route on the same name would be
// permanently shadowed by nginx's exact server_name and the admin UI would show
// no reason why.
func TestAPIProxyRouteRejectsDomainClaimedByReverseProxy(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	agentID := createProxyTestAgent(t, h, token, "egress-shadow")

	reverse := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-shadow-1", map[string]any{
		"agentId": agentID, "protocol": "http", "domain": "tp-shadow.tm.example.com", "pathPrefix": "/admin",
		"targetHost": "10.0.0.9", "targetPort": 8080,
	})
	if reverse.Code != http.StatusCreated {
		t.Fatalf("reverse-proxy create status=%d: %s", reverse.Code, reverse.Body.String())
	}

	shadowed := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-shadow-2", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-shadow.tm.example.com",
		"authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
	})
	if shadowed.Code != http.StatusConflict {
		t.Fatalf("proxy route on a claimed domain status=%d body=%s, want 409", shadowed.Code, shadowed.Body.String())
	}
}

func TestAPIProxyRouteValidationFailures(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	agentID := createProxyTestAgent(t, h, token, "egress-bad")

	base := func() map[string]any {
		return map[string]any{
			"agentId": agentID, "protocol": "http-proxy", "domain": "tp-ok.tm.example.com",
			"authMode": "none",
		}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing tp- prefix", func(m map[string]any) { m["domain"] = "demo.tm.example.com" }},
		{"missing suffix", func(m map[string]any) { m["domain"] = "tp-demo" }},
		{"underscore in name", func(m map[string]any) { m["domain"] = "tp-de_mo.tm.example.com" }},
		{"empty name", func(m map[string]any) { m["domain"] = "tp-.tm.example.com" }},
		{"name too long", func(m map[string]any) {
			m["domain"] = "tp-0123456789012345678901234567890123.tm.example.com"
		}},
		{"client supplied targetHost", func(m map[string]any) { m["targetHost"] = "10.0.0.1" }},
		{"client supplied targetPort", func(m map[string]any) { m["targetPort"] = 8080 }},
		{"client supplied config map", func(m map[string]any) { m["config"] = map[string]any{"authMode": "none"} }},
		{"basic without credential", func(m map[string]any) { m["authMode"] = "basic" }},
		{"unknown credential", func(m map[string]any) {
			m["authMode"] = "basic"
			m["credentialId"] = "cred-does-not-exist"
		}},
		{"unknown auth mode", func(m map[string]any) { m["authMode"] = "digest" }},
		{"invalid source cidr", func(m map[string]any) { m["sourceCIDRs"] = []string{"11.71.85.0/33"} }},
		{"invalid target cidr", func(m map[string]any) { m["targetCIDRs"] = []string{"not-a-cidr"} }},
		{"invalid target port", func(m map[string]any) { m["targetPorts"] = []int{70000} }},
		{"negative concurrency", func(m map[string]any) { m["maxConcurrentTunnels"] = -1 }},
		{"oversized description", func(m map[string]any) { m["description"] = string(make([]byte, 300)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := base()
			tc.mutate(body)
			resp := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "", body)
			if resp.Code != http.StatusBadRequest && resp.Code != http.StatusForbidden && resp.Code != http.StatusNotFound {
				t.Fatalf("%s: status=%d body=%s", tc.name, resp.Code, resp.Body.String())
			}
		})
	}
}

// Route creation is admin-only, so a non-admin caller is stopped by the role
// check before the credential is ever inspected. The cases that actually reach
// validateProxyCredential are the unusable-credential ones: foreign, disabled,
// deleted and wrong-typed references must all be refused without revealing
// whether the ID exists.
func TestAPIProxyRouteRejectsForeignCredential(t *testing.T) {
	api, admin, user := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	adminToken := apiToken(t, api, admin.Username, "admin-pass")
	userToken := apiToken(t, api, user.Username, "alice-pass")
	agentID := createProxyTestAgent(t, h, adminToken, "egress-owner")
	credentialID := createProxyTestCredential(t, h, adminToken, "admin proxy", "demo", "s3cret")

	foreign := apiJSON(t, h, http.MethodPost, "/api/v1/routes", userToken, "proxy-foreign-cred", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-alice.tm.example.com",
		"authMode": "basic", "credentialId": credentialID,
	})
	if foreign.Code != http.StatusForbidden && foreign.Code != http.StatusNotFound {
		t.Fatalf("non-admin route create status=%d body=%s", foreign.Code, foreign.Body.String())
	}

	disabled := apiJSON(t, h, http.MethodPatch, "/api/v1/credentials/"+credentialID, adminToken, "", map[string]any{"enabled": false})
	if disabled.Code != http.StatusOK {
		t.Fatalf("credential disable status=%d: %s", disabled.Code, disabled.Body.String())
	}
	bound := apiJSON(t, h, http.MethodPost, "/api/v1/routes", adminToken, "proxy-disabled-cred", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-disabled.tm.example.com",
		"authMode": "basic", "credentialId": credentialID,
	})
	if bound.Code != http.StatusBadRequest {
		t.Fatalf("disabled credential status=%d body=%s, want 400", bound.Code, bound.Body.String())
	}

	passwordCred := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", adminToken, "proxy-ssh-cred", map[string]any{
		"name": "host key", "type": "password", "enabled": true,
		"secret": map[string]any{"password": "s3cret"},
	})
	if passwordCred.Code != http.StatusCreated {
		t.Fatalf("password credential create status=%d: %s", passwordCred.Code, passwordCred.Body.String())
	}
	var password struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(passwordCred.Body.Bytes(), &password); err != nil {
		t.Fatal(err)
	}
	wrongType := apiJSON(t, h, http.MethodPost, "/api/v1/routes", adminToken, "proxy-wrong-type-cred", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-wrongtype.tm.example.com",
		"authMode": "basic", "credentialId": password.Data.ID,
	})
	if wrongType.Code != http.StatusBadRequest {
		t.Fatalf("password credential on a proxy route status=%d body=%s, want 400", wrongType.Code, wrongType.Body.String())
	}
}

func TestAPIProxyRouteUpdateKeepsSentinelAndRejectsProtocolSwitch(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	agentID := createProxyTestAgent(t, h, token, "egress-update")

	created := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-route-upd", map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-upd.tm.example.com",
		"authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d: %s", created.Code, created.Body.String())
	}
	var route proxyRouteResponse
	if err := json.Unmarshal(created.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}

	patched := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+route.Data.ID, token, "", map[string]any{
		"sourceCIDRs": []string{}, "allowPrivateTargets": false, "status": "disabled",
		"description": "closed for now",
	})
	if patched.Code != http.StatusOK {
		t.Fatalf("patch status=%d: %s", patched.Code, patched.Body.String())
	}
	var updated proxyRouteResponse
	if err := json.Unmarshal(patched.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Data.TargetHost != "*" || updated.Data.TargetPort != 0 || updated.Data.PathPrefix != "/" {
		t.Fatalf("sentinel lost on update: %+v", updated.Data)
	}
	if len(updated.Data.SourceCIDRs) != 0 {
		t.Fatalf("sourceCIDRs not cleared: %#v", updated.Data.SourceCIDRs)
	}
	if updated.Data.AllowPrivateTargets || updated.Data.Status != "disabled" || updated.Data.Description != "closed for now" {
		t.Fatalf("patched fields = %+v", updated.Data)
	}

	// Upstream options have no meaning on a proxy route; accepting them would
	// overwrite the policy JSON with normalizeUpstreamRouteConfig output.
	upstream := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+route.Data.ID, token, "", map[string]any{
		"hostHeader": "internal.example.com",
	})
	if upstream.Code != http.StatusBadRequest {
		t.Fatalf("hostHeader on a proxy route status=%d body=%s, want 400", upstream.Code, upstream.Body.String())
	}
	fixedTarget := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+route.Data.ID, token, "", map[string]any{
		"targetHost": "10.0.0.9",
	})
	if fixedTarget.Code != http.StatusBadRequest {
		t.Fatalf("targetHost on a proxy route status=%d body=%s, want 400", fixedTarget.Code, fixedTarget.Body.String())
	}

	switched := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+route.Data.ID, token, "", map[string]any{"protocol": "http"})
	if switched.Code != http.StatusBadRequest {
		t.Fatalf("protocol switch away from http-proxy status=%d body=%s", switched.Code, switched.Body.String())
	}
	toProxy := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "plain-route", map[string]any{
		"agentId": agentID, "protocol": "http", "domain": "app.tm.example.com", "pathPrefix": "/",
		"targetHost": "10.0.0.9", "targetPort": 8080,
	})
	if toProxy.Code != http.StatusCreated {
		t.Fatalf("plain route create status=%d: %s", toProxy.Code, toProxy.Body.String())
	}
	var plain proxyRouteResponse
	if err := json.Unmarshal(toProxy.Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	upgraded := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+plain.Data.ID, token, "", map[string]any{"protocol": "http-proxy"})
	if upgraded.Code != http.StatusBadRequest {
		t.Fatalf("protocol switch to http-proxy status=%d body=%s", upgraded.Code, upgraded.Body.String())
	}
	// A plain route must keep its existing update semantics.
	renamed := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+plain.Data.ID, token, "", map[string]any{"targetPort": 9090})
	if renamed.Code != http.StatusOK {
		t.Fatalf("plain route patch status=%d body=%s", renamed.Code, renamed.Body.String())
	}
}

func TestAPIProxyRouteIdempotentReplay(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	setCredentialSecretStore(t, api)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	agentID := createProxyTestAgent(t, h, token, "egress-idem")
	body := map[string]any{
		"agentId": agentID, "protocol": "http-proxy", "domain": "tp-idem.tm.example.com", "authMode": "none",
	}
	first := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-idem-key", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d: %s", first.Code, first.Body.String())
	}
	replay := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-idem-key", body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status=%d: %s", replay.Code, replay.Body.String())
	}
	var a, b proxyRouteResponse
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if a.Data.ID != b.Data.ID {
		t.Fatalf("idempotent replay created a second route: %s vs %s", a.Data.ID, b.Data.ID)
	}
}

func TestNormalizeProxyRouteConfigDefaults(t *testing.T) {
	encoded, message := normalizeProxyRouteConfig(proxyRouteConfig{})
	if message != "" {
		t.Fatalf("empty policy must be valid, got %q", message)
	}
	decoded, err := proxyRouteConfigOf(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.AuthMode != "none" || decoded.AllowPrivateTargets == nil || !*decoded.AllowPrivateTargets {
		t.Fatalf("defaults = %+v", decoded)
	}
	if decoded.SourceCIDRs == nil || decoded.TargetCIDRs == nil || decoded.TargetPorts == nil {
		t.Fatalf("empty allowlists must round-trip as [] not null: %s", encoded)
	}
	if _, message := normalizeProxyRouteConfig(proxyRouteConfig{AuthMode: "basic"}); message == "" {
		t.Fatal("basic without credentialId must be rejected")
	}
	if _, message := normalizeProxyRouteConfig(proxyRouteConfig{AuthMode: "BASIC", CredentialID: "c1"}); message != "" {
		t.Fatalf("authMode must be case-insensitive, got %q", message)
	}
	if _, message := normalizeProxyRouteConfig(proxyRouteConfig{Description: string(make([]byte, 300))}); message == "" {
		t.Fatal("oversized description must be rejected")
	}
	if _, message := normalizeProxyRouteConfig(proxyRouteConfig{TargetPorts: []int{0}}); message == "" {
		t.Fatal("port 0 must be rejected")
	}
	// authMode none must drop a stale credential reference, otherwise disabling
	// authentication would keep the data plane resolving a secret it never uses.
	encoded, message = normalizeProxyRouteConfig(proxyRouteConfig{AuthMode: "none", CredentialID: "c1"})
	if message != "" {
		t.Fatalf("none with a credentialId must be accepted, got %q", message)
	}
	decoded, err = proxyRouteConfigOf(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CredentialID != "" {
		t.Fatalf("credentialId survived authMode none: %+v", decoded)
	}
	// A route the data plane could not build must never be stored.
	if _, message := normalizeProxyRouteConfig(proxyRouteConfig{SourceCIDRs: []string{"10.0.0.0/8", "10.0.0.0/8"}}); message != "" {
		t.Fatalf("duplicate CIDRs must be accepted, got %q", message)
	}
	deduped, _ := normalizeProxyRouteConfig(proxyRouteConfig{SourceCIDRs: []string{"10.0.0.0/8", "10.0.0.0/8"}})
	var round proxyRouteConfig
	if err := json.Unmarshal([]byte(deduped), &round); err != nil {
		t.Fatal(err)
	}
	if len(round.SourceCIDRs) != 1 {
		t.Fatalf("duplicate CIDRs were kept: %#v", round.SourceCIDRs)
	}
	if _, err := proxyRouteConfigOf("not json"); err == nil {
		t.Fatal("corrupt config must fail to decode")
	}
	empty, err := proxyRouteConfigOf("")
	if err != nil || empty.AuthMode != "none" || empty.AllowPrivateTargets == nil || !*empty.AllowPrivateTargets {
		t.Fatalf("empty config defaults = %+v err = %v", empty, err)
	}
}
