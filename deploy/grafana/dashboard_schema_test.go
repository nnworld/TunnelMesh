package grafana_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type panel struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	Datasource any    `json:"datasource"`
	Targets    []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
	Panels []panel `json:"panels"`
}

// variableLabels binds every query-backed dashboard variable to the Prometheus
// label it must enumerate. A variable that enumerates one label while panels
// filter another renders empty series; the failure stays invisible while the
// default "All" selection expands to .*, so it only surfaces once an operator
// picks a concrete value.
//
// cluster and node_id are deployment-side labels: no TunnelMesh metric carries
// them, they are injected by the Prometheus scrape config so that every series
// from a Server target can be scoped per cluster and per node. component and
// agent_id are emitted by the application itself.
var variableLabels = map[string]string{
	"cluster":   "cluster",
	"node_id":   "node_id",
	"component": "component",
	"agent_id":  "agent_id",
}

// labelValuesQuery matches label_values(<metric>, <label>).
var labelValuesQuery = regexp.MustCompile(`^label_values\(\s*([a-zA-Z_:][a-zA-Z0-9_:]*)\s*,\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\)$`)

// variableSelector matches a PromQL label matcher bound to a dashboard
// variable, for example component=~"$component" or agent_id="$agent_id".
var variableSelector = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*=~?\s*"\$([a-zA-Z_][a-zA-Z0-9_]*)"`)

func TestTunnelMeshDashboardVariablesMatchFilteredLabels(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("dashboards", "tunnelmesh.json"))
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	var dashboard struct {
		Panels     []panel `json:"panels"`
		Templating struct {
			List []struct {
				Name  string `json:"name"`
				Type  string `json:"type"`
				Query string `json:"query"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(b, &dashboard); err != nil {
		t.Fatalf("decode dashboard JSON: %v", err)
	}

	queries := map[string]string{}
	for _, variable := range dashboard.Templating.List {
		queries[variable.Name] = strings.TrimSpace(variable.Query)
	}
	for name, wantLabel := range variableLabels {
		query, ok := queries[name]
		if !ok {
			t.Errorf("missing dashboard variable %q", name)
			continue
		}
		match := labelValuesQuery.FindStringSubmatch(query)
		if match == nil {
			t.Errorf("variable %q query %q must be label_values(<metric>, %s)", name, query, wantLabel)
			continue
		}
		if match[2] != wantLabel {
			t.Errorf("variable %q enumerates label %q, want %q", name, match[2], wantLabel)
		}
	}

	exprs := map[string][]string{}
	var collect func([]panel)
	collect = func(panels []panel) {
		for _, item := range panels {
			for _, target := range item.Targets {
				exprs[item.Title] = append(exprs[item.Title], target.Expr)
			}
			collect(item.Panels)
		}
	}
	collect(dashboard.Panels)

	for title, list := range exprs {
		for _, expr := range list {
			for _, match := range variableSelector.FindAllStringSubmatch(expr, -1) {
				label, name := match[1], match[2]
				wantLabel, ok := variableLabels[name]
				if !ok {
					continue
				}
				if label != wantLabel {
					t.Errorf("panel %q filters label %q by $%s, which enumerates %q: %s", title, label, name, wantLabel, expr)
				}
			}
		}
	}
}

// TestPrometheusExampleInjectsDashboardScopeLabels guards the cross-artifact
// contract behind $cluster and $node_id: no TunnelMesh metric carries those
// labels, so the dashboard can only scope by them when the scrape config
// injects them per target. Dropping them from the example silently degrades
// every node-scoped panel back to a cluster-wide aggregate.
func TestPrometheusExampleInjectsDashboardScopeLabels(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "prometheus", "prometheus.yml.example"))
	if err != nil {
		t.Fatalf("read prometheus example: %v", err)
	}
	injected := map[string]bool{}
	inLabels := false
	labelsIndent := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if inLabels && indent <= labelsIndent {
			inLabels = false
		}
		if trimmed == "labels:" {
			inLabels, labelsIndent = true, indent
			continue
		}
		if inLabels && indent > labelsIndent {
			if key, _, found := strings.Cut(trimmed, ":"); found {
				injected[strings.TrimSpace(key)] = true
			}
		}
	}
	for _, label := range []string{"cluster", "node_id"} {
		if !injected[label] {
			t.Errorf("prometheus.yml.example must inject the %q target label required by the dashboard scope variables", label)
		}
	}
}

func TestTunnelMeshDashboardSchema(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("dashboards", "*.json"))
	if err != nil {
		t.Fatalf("list dashboards: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "tunnelmesh.json" {
		t.Fatalf("expected exactly one dashboard JSON, got %v", files)
	}
	root := filepath.Join("dashboards", "tunnelmesh.json")
	b, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	var dashboard struct {
		UID        string  `json:"uid"`
		Title      string  `json:"title"`
		Panels     []panel `json:"panels"`
		Templating struct {
			List []struct {
				Name    string `json:"name"`
				Query   string `json:"query"`
				Options []any  `json:"options"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(b, &dashboard); err != nil {
		t.Fatalf("decode dashboard JSON: %v", err)
	}
	if dashboard.UID == "" || dashboard.Title == "" {
		t.Fatalf("dashboard must have uid and title")
	}
	if strings.Count(string(b), `"type": "row"`) != 6 {
		t.Fatalf("dashboard must contain exactly six row panels")
	}
	for _, row := range []string{"Overview", "Agent", "Network", "Cluster", "Security", "HTTP Proxy Entry"} {
		if !strings.Contains(string(b), `"title": "`+row+`"`) {
			t.Fatalf("missing row %q", row)
		}
	}
	for _, variable := range []string{"cluster", "component", "node_id", "agent_id"} {
		found := false
		for _, item := range dashboard.Templating.List {
			if item.Name == variable {
				found = true
				if item.Query == "" && len(item.Options) == 0 {
					t.Fatalf("variable %q must be bounded by query or options", variable)
				}
			}
		}
		if !found {
			t.Fatalf("missing variable %q", variable)
		}
	}
	var validatePanels func([]panel)
	allExpr := strings.Builder{}
	validatePanels = func(panels []panel) {
		for _, panel := range panels {
			if panel.Type != "row" && panel.Datasource != "${DS_PROMETHEUS}" {
				t.Errorf("panel %q must use ${DS_PROMETHEUS}", panel.Title)
			}
			for _, target := range panel.Targets {
				allExpr.WriteString(target.Expr)
				allExpr.WriteByte('\n')
				if strings.TrimSpace(target.Expr) == "" {
					t.Errorf("panel %q has an empty PromQL target", panel.Title)
				}
				for _, forbidden := range []string{"token_id", "connection_id", "stream_id", "bearer", "metadata"} {
					if strings.Contains(strings.ToLower(target.Expr), forbidden) {
						t.Errorf("panel %q contains forbidden label %q", panel.Title, forbidden)
					}
				}
			}
			validatePanels(panel.Panels)
		}
	}
	validatePanels(dashboard.Panels)
	for _, variable := range []string{"cluster", "component", "node_id", "agent_id"} {
		if !strings.Contains(allExpr.String(), "$"+variable) {
			t.Errorf("variable %q is not used by any panel query", variable)
		}
	}
	for _, metric := range []string{
		"tunnelmesh_stream_stage_duration_seconds",
		"tunnelmesh_stream_open_total",
		"tunnelmesh_stream_queue_wait_seconds",
		"tunnelmesh_stream_window_stall_seconds",
		"tunnelmesh_stream_backpressure_total",
		"tunnelmesh_authorization_cache_requests_total",
		"tunnelmesh_authorization_revision",
		"tunnelmesh_authorization_revision_poll_total",
		"tunnelmesh_remote_validation_cache_requests_total",
		"tunnelmesh_agent_connection_scale_decisions_total",
		"tunnelmesh_agent_selection_total",
		"tunnelmesh_proxy_entry_requests_total",
		"tunnelmesh_proxy_entry_tunnels_active",
		"tunnelmesh_proxy_entry_tunnel_duration_seconds",
		"tunnelmesh_proxy_entry_auth_failures_total",
		"tunnelmesh_proxy_entry_acl_denied_total",
	} {
		if !strings.Contains(allExpr.String(), metric) {
			t.Errorf("dashboard is missing required metric %q", metric)
		}
	}
}
