package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type metadataTestTransport struct{ closed bool }

func (t *metadataTestTransport) Send(protocol.Frame) error { return nil }
func (t *metadataTestTransport) Receive() (protocol.Frame, error) {
	return protocol.Frame{}, errors.New("not used")
}
func (t *metadataTestTransport) Close() error {
	t.closed = true
	return nil
}

func TestMetadataCollectorReadsAllowlistedFileAndEnvInDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device-id")
	if err := os.WriteFile(path, []byte("设备-01\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNELMESH_REGION", "ap-east")
	collector := NewMetadataCollector([]config.MetadataSource{
		{Name: "region", Source: "env", Key: "TUNNELMESH_REGION"},
		{Name: "device_id", Source: "file", Path: path},
	})
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got := snapshot.Values["device_id"]; got != "设备-01" {
		t.Fatalf("device_id = %q", got)
	}
	if got := snapshot.Values["region"]; got != "ap-east" {
		t.Fatalf("region = %q", got)
	}
	if len(snapshot.Fields) != 2 || snapshot.Fields[0].Name != "device_id" || snapshot.Fields[1].Name != "region" {
		t.Fatalf("fields are not deterministic: %+v", snapshot.Fields)
	}
}

func TestMetadataCollectorReportsMissingSourceWithoutFailingCollection(t *testing.T) {
	collector := NewMetadataCollector([]config.MetadataSource{{Name: "missing", Source: "env", Key: "TUNNELMESH_NOT_SET"}})
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v, want per-field error only", err)
	}
	if len(snapshot.Errors) != 1 || snapshot.Errors[0].Name != "missing" {
		t.Fatalf("errors = %+v", snapshot.Errors)
	}
}

func TestSessionMetadataErrorsDoNotCloseTransport(t *testing.T) {
	transport := &metadataTestTransport{}
	session := NewSessionWithMetadata(transport, NewMetadataCollector([]config.MetadataSource{{Name: "missing", Source: "env", Key: "TUNNELMESH_NOT_SET"}}))
	snapshot, err := session.CollectMetadata(context.Background())
	if err != nil || len(snapshot.Errors) != 1 {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}
	if transport.closed {
		t.Fatal("metadata collection closed the active session transport")
	}
}

func TestMetadataCollectorRejectsFieldAndAggregateLimits(t *testing.T) {
	t.Setenv("TUNNELMESH_BIG", strings.Repeat("x", DefaultMetadataFieldBytes+1))
	collector := NewMetadataCollector([]config.MetadataSource{{Name: "big", Source: "env", Key: "TUNNELMESH_BIG"}})
	snapshot, err := collector.Collect(context.Background())
	if err != nil || len(snapshot.Errors) != 1 || !strings.Contains(strings.ToLower(snapshot.Errors[0].Error), "field") {
		t.Fatalf("field limit snapshot=%+v error=%v", snapshot, err)
	}

	values := make([]config.MetadataSource, 0, DefaultMetadataMaxFields)
	for i := 0; i < DefaultMetadataMaxFields; i++ {
		key := "TUNNELMESH_META_" + string(rune('A'+i))
		t.Setenv(key, strings.Repeat("v", DefaultMetadataFieldBytes))
		values = append(values, config.MetadataSource{Name: "field_" + string(rune('a'+i)), Source: "env", Key: key})
	}
	collector = NewMetadataCollector(values)
	collector.MaxPayloadBytes = DefaultMetadataPayloadBytes - 1
	if _, err := collector.Collect(context.Background()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "payload") {
		t.Fatalf("payload limit error = %v", err)
	}
}

func TestMetadataCollectorHonorsCancellationAndInvalidUTF8(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collector := NewMetadataCollector(nil)
	if _, err := collector.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() error = %v, want context cancellation", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "invalid")
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	collector = NewMetadataCollector([]config.MetadataSource{{Name: "invalid", Source: "file", Path: path}})
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(snapshot.Errors) != 1 || !strings.Contains(strings.ToLower(snapshot.Errors[0].Error), "utf") {
		t.Fatalf("errors = %+v", snapshot.Errors)
	}
}
