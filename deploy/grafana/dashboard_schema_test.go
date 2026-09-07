package grafana_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if strings.Count(string(b), `"type": "row"`) != 5 {
		t.Fatalf("dashboard must contain exactly five row panels")
	}
	for _, row := range []string{"Overview", "Agent", "Network", "Cluster", "Security"} {
		if !strings.Contains(string(b), `"title": "`+row+`"`) {
			t.Fatalf("missing row %q", row)
		}
	}
	for _, variable := range []string{"cluster", "node_id", "agent_id"} {
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
	for _, variable := range []string{"cluster", "node_id", "agent_id"} {
		if !strings.Contains(allExpr.String(), "$"+variable) {
			t.Errorf("variable %q is not used by any panel query", variable)
		}
	}
}
