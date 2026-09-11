package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/metadata"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type metadataTestTransport struct {
	closed bool
	sent   []protocol.Frame
}

func (t *metadataTestTransport) Send(frame protocol.Frame) error {
	t.sent = append(t.sent, frame)
	return nil
}
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

func TestSessionReportsHelloUpdatesAndReconnectSnapshot(t *testing.T) {
	t.Setenv("TUNNELMESH_REGION", "cn-east")
	transport := &metadataTestTransport{}
	session := NewSessionWithMetadata(transport, NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_REGION"}}))
	session.SetMetadataIdentity("agent-1", "node-1", 8)
	if err := session.ReportMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.sent) != 1 || transport.sent[0].Type != protocol.FrameAgentHello {
		t.Fatalf("initial frames=%+v", transport.sent)
	}
	payload, err := protocol.DecodeAgentMetadataPayload(transport.sent[0].Payload)
	if err != nil || payload.Revision != 1 || payload.AgentID != "agent-1" {
		t.Fatalf("hello payload=%+v err=%v", payload, err)
	}
	if err := session.ReportMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.sent) != 1 {
		t.Fatalf("unchanged snapshot emitted an update: %+v", transport.sent)
	}
	t.Setenv("TUNNELMESH_REGION", "cn-north")
	if err := session.ReportMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.sent) != 2 || transport.sent[1].Type != protocol.FrameAgentMetadataUpdate {
		t.Fatalf("changed frames=%+v", transport.sent)
	}
	updated, err := protocol.DecodeAgentMetadataPayload(transport.sent[1].Payload)
	if err != nil || updated.Revision != 2 || updated.Items[0].Value != "cn-north" {
		t.Fatalf("update payload=%+v err=%v", updated, err)
	}
	session.ResetMetadataReport()
	if err := session.ReportMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.sent) != 3 || transport.sent[2].Type != protocol.FrameAgentHello {
		t.Fatalf("reconnect frames=%+v", transport.sent)
	}
}

func TestSessionIncludesConfiguredCapabilitiesInMetadata(t *testing.T) {
	transport := &metadataTestTransport{}
	session := NewSessionWithMetadata(transport, NewMetadataCollector(nil))
	session.SetCapabilities([]string{protocol.CapabilityStreamOpenResult})
	if err := session.ReportMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.DecodeAgentMetadataPayload(transport.sent[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Capabilities) != 1 || payload.Capabilities[0] != protocol.CapabilityStreamOpenResult {
		t.Fatalf("hello capabilities=%v", payload.Capabilities)
	}
}

func TestSessionMetadataStateIsSafeDuringConcurrentReportingAndReset(t *testing.T) {
	t.Setenv("TUNNELMESH_REGION", "cn-east")
	session := NewSessionWithMetadata(&metadataTestTransport{}, NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_REGION"}}))
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				switch worker {
				case 0:
					session.SetMetadataIdentity("agent", "node", int64(j+1))
				case 1:
					session.ResetMetadataReport()
				case 2:
					_ = session.ReportMetadata(context.Background())
				}
			}
		}(i)
	}
	wg.Wait()
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

func TestMetadataCollectorRejectsExpandedSensitiveNames(t *testing.T) {
	collector := NewMetadataCollector([]config.MetadataSource{
		{Name: "authorization_header", Source: "env", Key: "TUNNELMESH_AUTHORIZATION"},
	})
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Errors) != 1 || !strings.Contains(strings.ToLower(snapshot.Errors[0].Error), "sensitive") {
		t.Fatalf("errors = %+v, want expanded sensitive-name rejection", snapshot.Errors)
	}
}

func TestMetadataCollectorNilReceiverReturnsEmptySnapshot(t *testing.T) {
	var collector *MetadataCollector
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Fields) != 0 || len(snapshot.Values) != 0 {
		t.Fatalf("nil collector snapshot = %+v, want empty snapshot", snapshot)
	}
}

func TestOversizedEnvironmentValueIsRejectedBeforeCopy(t *testing.T) {
	t.Setenv("TUNNELMESH_HUGE", strings.Repeat("x", DefaultMetadataFieldBytes+1))
	source := config.MetadataSource{Name: "huge", Source: "env", Key: "TUNNELMESH_HUGE"}
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = metadata.ReadSource(context.Background(), source, DefaultMetadataFieldBytes)
	})
	t.Logf("oversized environment read allocations = %.2f", allocs)
	if allocs > 1 {
		t.Fatalf("oversized environment read allocated %.2f times; expected the value copy to be avoided", allocs)
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
