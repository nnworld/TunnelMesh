package metadata

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/inventory"
)

// Built-in inventory is part of every snapshot, with no configuration needed.
func TestCollectAlwaysIncludesBuiltinInventory(t *testing.T) {
	snapshot, err := NewCollector(nil).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	raw, ok := snapshot.Values[inventory.KeyOS]
	if !ok {
		t.Fatalf("snapshot has no %s: %v", inventory.KeyOS, keys(snapshot.Values))
	}
	var osInfo inventory.OSInfo
	if err := json.Unmarshal([]byte(raw), &osInfo); err != nil || osInfo.Arch == "" {
		t.Fatalf("os value = %q (err %v)", raw, err)
	}
	for _, field := range snapshot.Fields {
		if field.Name == inventory.KeyOS && field.Source != "inventory" {
			t.Fatalf("builtin field source = %q, want inventory", field.Source)
		}
	}
}

// An operator-declared name wins over a built-in one of the same name, so a site can
// override what the agent reports without the snapshot ever holding two values.
func TestOperatorSourceOverridesBuiltinInventory(t *testing.T) {
	t.Setenv("TM_TEST_OS_OVERRIDE", "custom-os")
	sources := []Source{{Name: inventory.KeyOS, Source: "env", Key: "TM_TEST_OS_OVERRIDE"}}
	snapshot, err := NewCollector(sources).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if snapshot.Values[inventory.KeyOS] != "custom-os" {
		t.Fatalf("value = %q, want the operator's", snapshot.Values[inventory.KeyOS])
	}
	count := 0
	for _, field := range snapshot.Fields {
		if field.Name == inventory.KeyOS {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%s appears %d times, want once", inventory.KeyOS, count)
	}
}

func keys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

// Built-in facts take only the room the operator's sources leave, so a snapshot
// never exceeds the field count or payload the server will accept.
func TestInventoryRespectsFieldAndPayloadLimits(t *testing.T) {
	collector := NewCollector(nil)
	collector.MaxFields = 2
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snapshot.Fields) > 2 {
		t.Fatalf("fields = %d, want at most 2", len(snapshot.Fields))
	}

	collector = NewCollector(nil)
	collector.MaxPayloadBytes = 256
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect under a tight payload: %v", err)
	}
	if snapshot.PayloadBytes > 256 {
		t.Fatalf("payload = %d bytes, want at most 256", snapshot.PayloadBytes)
	}
}
