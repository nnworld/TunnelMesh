package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// The list status column rendered the enabled flag, so an agent created in the
// console showed as online before it ever connected. Connectivity must come
// from unexpired connection leases, the same fact the dashboard counts.
func TestListAgentsReportsConnectivityStatus(t *testing.T) {
	api, _, owner := apiTestServer(t)
	ctx := context.Background()
	agent := storage.Agent{ID: "agent-list-status", Name: "status-agent", OwnerUserID: owner.ID, Capabilities: `[]`, Enabled: true}
	if err := api.DB.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	token := apiToken(t, api, owner.Username, "alice-pass")

	statusOf := func(t *testing.T) string {
		t.Helper()
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents", token, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, item := range envelope.Data.Items {
			if item["id"] == agent.ID {
				status, _ := item["status"].(string)
				return status
			}
		}
		t.Fatalf("agent %s missing from list body %s", agent.ID, response.Body.String())
		return ""
	}

	if got := statusOf(t); got != "offline" {
		t.Fatalf("never-connected agent status = %q, want offline", got)
	}

	lease := storage.AgentLease{
		AgentID: agent.ID, NodeID: "node-1", InstanceID: "instance-1", ConnectionID: "conn-1",
		ServerNodeID: "server-1", Epoch: 1, ConnectionEpoch: 1,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if _, err := api.DB.Leases().RegisterConnection(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t); got != "online" {
		t.Fatalf("leased agent status = %q, want online", got)
	}

	if err := api.DB.Leases().ReleaseConnection(ctx, agent.ID, "conn-1", 1); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t); got != "offline" {
		t.Fatalf("released agent status = %q, want offline", got)
	}
}

// agentListRow is one item of the list envelope, kept as JSON fields so the test
// reads what the console reads rather than a Go-only projection.
type agentListRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
}

func (r agentListRow) String() string { return r.ID + "@" + r.CreatedAt }

// readAgentPage issues one list request and returns the page it got back.
func readAgentPage(t *testing.T, handler http.Handler, path, token string) ([]agentListRow, string, bool) {
	t.Helper()
	response := apiJSON(t, handler, http.MethodGet, path, token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Items      []agentListRow `json:"items"`
			NextCursor string         `json:"nextCursor"`
			HasMore    bool           `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data.Items, envelope.Data.NextCursor, envelope.Data.HasMore
}

func seedAgent(t *testing.T, api *API, id, name, ownerID string, createdAt time.Time) {
	t.Helper()
	agent := storage.Agent{ID: id, Name: name, OwnerUserID: ownerID, Capabilities: `[]`, Enabled: true, CreatedAt: createdAt}
	if err := api.DB.Agents().Create(context.Background(), agent); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func agentRowByID(items []agentListRow, id string) (agentListRow, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return agentListRow{}, false
}

// The console defaults to newest first and 20 rows per page. Only the server can
// deliver that: a page ordered by id and re-sorted in the browser shows the
// oldest registered agents, which is the opposite of what an operator scans for
// after adding a node.
func TestListAgentsPagesNewestFirst(t *testing.T) {
	api, _, owner := apiTestServer(t)
	adminToken := apiToken(t, api, "admin", "admin-pass")
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedAgent(t, api, "agent-page-a", "page-alpha", owner.ID, base)
	seedAgent(t, api, "agent-page-b", "page-beta", owner.ID, base.Add(24*time.Hour))
	seedAgent(t, api, "agent-page-c", "page-gamma", owner.ID, base.Add(48*time.Hour))

	first, cursor, hasMore := readAgentPage(t, api.Handler(), "/api/v1/agents?limit=2", adminToken)
	if len(first) != 2 || !hasMore || cursor == "" {
		t.Fatalf("first page = %v cursor=%q hasMore=%v", first, cursor, hasMore)
	}
	if first[0].CreatedAt <= first[1].CreatedAt {
		t.Fatalf("first page is not newest first: %v then %v", first[0], first[1])
	}
	if _, ok := agentRowByID(first, "agent-page-c"); !ok {
		t.Fatalf("newest agent missing from the first page: %v", first)
	}

	second, secondCursor, secondHasMore := readAgentPage(t, api.Handler(), "/api/v1/agents?limit=2&cursor="+url.QueryEscape(cursor), adminToken)
	if _, ok := agentRowByID(second, "agent-page-a"); !ok {
		t.Fatalf("second page dropped the oldest agent: %v", second)
	}
	for _, item := range second {
		if item.ID == "agent-page-c" || item.ID == "agent-page-b" {
			t.Fatalf("cursor replayed %q across pages: %v", item.ID, second)
		}
	}
	if secondHasMore || secondCursor != "" {
		t.Fatalf("last page reports more rows: %v cursor=%q", second, secondCursor)
	}
}

// A keyword has to narrow inside the paging semantics. Filtering the fetched page
// in the browser hides every match past the first page, which is what the console
// had to do before this parameter existed.
func TestListAgentsKeywordFiltersInSQL(t *testing.T) {
	api, _, owner := apiTestServer(t)
	adminToken := apiToken(t, api, "admin", "admin-pass")
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedAgent(t, api, "agent-keyword-1", "searchable-one", owner.ID, base)
	seedAgent(t, api, "agent-keyword-2", "searchable-two", owner.ID, base.Add(time.Hour))
	seedAgent(t, api, "agent-keyword-3", "quiet-three", owner.ID, base.Add(2*time.Hour))

	matched, _, _ := readAgentPage(t, api.Handler(), "/api/v1/agents?limit=10&keyword=searchable", adminToken)
	if len(matched) != 2 {
		t.Fatalf("keyword page = %v, want the two searchable agents", matched)
	}
	byID, _, _ := readAgentPage(t, api.Handler(), "/api/v1/agents?limit=10&keyword=agent-keyword-3", adminToken)
	if len(byID) != 1 || byID[0].ID != "agent-keyword-3" {
		t.Fatalf("id keyword page = %v, want only agent-keyword-3", byID)
	}
	none, _, more := readAgentPage(t, api.Handler(), "/api/v1/agents?limit=10&keyword=no-such-agent", adminToken)
	if len(none) != 0 || more {
		t.Fatalf("unmatched keyword returned %v hasMore=%v", none, more)
	}
}

// Non-admin visibility must be a SQL predicate rather than a post-filter over a
// 500 row window: the previous loop could miss an owner's agent beyond that
// window and hand back a cursor that replayed rows the caller could not see.
func TestListAgentsScopesNonAdminPageToOwner(t *testing.T) {
	api, admin, owner := apiTestServer(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		seedAgent(t, api, fmt.Sprintf("agent-foreign-%d", i), "foreign name", admin.ID, base.Add(time.Duration(i)*time.Minute))
	}
	seedAgent(t, api, "agent-mine-1", "mine one", owner.ID, base.Add(time.Hour))
	seedAgent(t, api, "agent-mine-2", "mine two", owner.ID, base.Add(2*time.Hour))
	token := apiToken(t, api, owner.Username, "alice-pass")

	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		path := "/api/v1/agents?limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		items, next, hasMore := readAgentPage(t, api.Handler(), path, token)
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("agent %q returned twice while paging as a normal user: %v", item.ID, seen)
			}
			seen[item.ID] = true
		}
		if !hasMore || next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 2 {
		t.Fatalf("normal user saw %v, want only the two agents they own", seen)
	}
	for id := range seen {
		if !strings.HasPrefix(id, "agent-mine-") {
			t.Fatalf("normal user saw %q", id)
		}
	}
}

// The console edit dialog sends the name and the administrative switch together,
// and an operator can change either one alone. Both halves matter: a PATCH that
// omitted `enabled` must not silently re-enable a node, and rejecting a
// non-admin here is what keeps the switch out of reach for owners who may create
// nothing either.
func TestUpdateAgentNameAndEnabledViaPatch(t *testing.T) {
	api, _, owner := apiTestServer(t)
	adminToken := apiToken(t, api, "admin", "admin-pass")
	ownerToken := apiToken(t, api, owner.Username, "alice-pass")
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedAgent(t, api, "agent-edit-1", "before rename", owner.ID, base)

	decode := func(t *testing.T, recorder *httptest.ResponseRecorder) (string, bool) {
		t.Helper()
		var envelope struct {
			Data struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data.Name, envelope.Data.Enabled
	}

	recorder := apiJSON(t, api.Handler(), http.MethodPatch, "/api/v1/agents/agent-edit-1", adminToken, "", map[string]any{"name": "after rename", "enabled": false})
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	name, enabled := decode(t, recorder)
	if name != "after rename" || enabled {
		t.Fatalf("patch response = name %q enabled %v, want the new name and enabled=false", name, enabled)
	}

	items, _, _ := readAgentPage(t, api.Handler(), "/api/v1/agents?keyword=after%20rename", adminToken)
	row, ok := agentRowByID(items, "agent-edit-1")
	if !ok || row.Enabled {
		t.Fatalf("list row after disabling = %+v (found=%v), want the renamed agent with enabled=false", row, ok)
	}

	// A name-less PATCH carries only the switch, and must leave the stored name
	// alone rather than rewriting it from a zero value.
	recorder = apiJSON(t, api.Handler(), http.MethodPatch, "/api/v1/agents/agent-edit-1", adminToken, "", map[string]any{"enabled": true})
	if recorder.Code != http.StatusOK {
		t.Fatalf("re-enable status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if name, enabled = decode(t, recorder); name != "after rename" || !enabled {
		t.Fatalf("re-enable response = name %q enabled %v, want the stored name and enabled=true", name, enabled)
	}

	if forbidden := apiJSON(t, api.Handler(), http.MethodPatch, "/api/v1/agents/agent-edit-1", ownerToken, "", map[string]any{"enabled": false}); forbidden.Code != http.StatusForbidden {
		t.Fatalf("owner patch status = %d, want 403", forbidden.Code)
	}
}
