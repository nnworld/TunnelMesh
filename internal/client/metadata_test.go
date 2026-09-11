package client_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestClientMetadataCollectorBuildsSafeSnapshot(t *testing.T) {
	collector, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
		InstanceID: "client-0123456789abcdef0123456789abcdef",
		AgentIDs:   []string{"agent-0123456789abcdef"},
		Version:    "v1.2.3",
		Commit:     "0123456789abcdef",
		Listeners: []protocol.ClientListener{{
			Protocol: "socks5", ListenAddress: "127.0.0.1:10866", AgentID: "agent-0123456789abcdef", Enabled: true,
		}},
		Metadata: []client.MetadataField{{Name: "region", Source: "static", Value: "cn-north"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := collector.Snapshot(context.Background())
	if snapshot.InstanceID != "client-0123456789abcdef0123456789abcdef" || snapshot.Version != "v1.2.3" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(snapshot.Listeners) != 1 || snapshot.Listeners[0].Protocol != "socks5" {
		t.Fatalf("listeners = %#v", snapshot.Listeners)
	}
}

func TestClientMetadataCollectorRejectsSensitiveName(t *testing.T) {
	_, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
		InstanceID: "client-0123456789abcdef0123456789abcdef",
		Metadata:   []client.MetadataField{{Name: "api_token", Source: "static", Value: "must-not-be-sent"}},
	})
	if err == nil {
		t.Fatal("expected sensitive metadata name to be rejected")
	}
}

func TestClientMetadataCollectorRejectsExpandedSensitiveName(t *testing.T) {
	_, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
		InstanceID: "client-0123456789abcdef0123456789abcdef",
		Metadata:   []client.MetadataField{{Name: "credential", Source: "static", Value: "must-not-be-sent"}},
	})
	if err == nil {
		t.Fatal("expected expanded sensitive metadata name to be rejected")
	}
}

func TestClientMetadataCollectorRejectsAggregatePayloadLimit(t *testing.T) {
	metadata := make([]client.MetadataField, 0, 8)
	for i := 0; i < 8; i++ {
		metadata = append(metadata, client.MetadataField{
			Name: "field_" + string(rune('a'+i)), Source: "static", Value: strings.Repeat("x", client.DefaultMetadataFieldBytes),
		})
	}
	_, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
		InstanceID: "client-0123456789abcdef0123456789abcdef",
		Metadata:   metadata,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "payload") {
		t.Fatalf("NewClientMetadataCollector() error = %v, want aggregate payload limit error", err)
	}
}

func TestClientMetadataCollectorRejectsDuplicateMetadataNames(t *testing.T) {
	_, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
		InstanceID: "client-0123456789abcdef0123456789abcdef",
		Metadata: []client.MetadataField{
			{Name: "region", Source: "file", Value: "cn-north"},
			{Name: "region", Source: "env", Value: "cn-east"},
		},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		t.Fatalf("NewClientMetadataCollector() error = %v, want duplicate metadata name error", err)
	}
}
