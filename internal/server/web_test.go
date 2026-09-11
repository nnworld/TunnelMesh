package server

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedWebDistContainsAgentMetadataUI(t *testing.T) {
	var bundle strings.Builder
	err := fs.WalkDir(webDist, "web_dist/assets", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			return nil
		}
		data, err := fs.ReadFile(webDist, path)
		if err != nil {
			return err
		}
		bundle.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	content := bundle.String()
	for _, keyword := range []string{"Agent details", "Redacted", "includeStale", "Access policies", "Delete access policy", "Access policy restored", "restore-agent-policy"} {
		if !strings.Contains(content, keyword) {
			t.Fatalf("embedded web bundle does not contain %q", keyword)
		}
	}
}
