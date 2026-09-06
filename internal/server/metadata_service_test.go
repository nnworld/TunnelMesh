package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestAgentMetadataServiceValidatesAndRedacts(t *testing.T) {
	repo := &fakeMetadataRepo{}
	svc := NewAgentMetadataService(repo)
	input := AgentMetadataInput{AgentID: "agent-1", NodeID: "node-a", Epoch: 1, Revision: 1, Items: []MetadataItem{{Name: "region", Source: "env", Value: "cn"}, {Name: "api_token", Source: "env", Value: "secret"}}, ReportedAt: time.Now().UTC()}
	if _, err := svc.Upsert(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(repo.value.Metadata, `"redacted":true`) {
		t.Fatalf("metadata=%s", repo.value.Metadata)
	}
	if strings.Contains(repo.value.Metadata, "secret") {
		t.Fatalf("sensitive value persisted: %s", repo.value.Metadata)
	}
}

func TestAgentMetadataServiceRejectsInvalidSourceAndOversizedPayload(t *testing.T) {
	svc := NewAgentMetadataService(&fakeMetadataRepo{})
	if _, err := svc.Upsert(context.Background(), AgentMetadataInput{AgentID: "a", NodeID: "n", Epoch: 1, Revision: 1, Items: []MetadataItem{{Name: "x", Source: "command", Value: "bad"}}}); err == nil {
		t.Fatal("invalid source accepted")
	}
	if _, err := svc.Upsert(context.Background(), AgentMetadataInput{AgentID: "a", NodeID: "n", Epoch: 1, Revision: 1, Items: []MetadataItem{{Name: "x", Source: "env", Value: strings.Repeat("x", 40<<10)}}}); err == nil {
		t.Fatal("oversized metadata accepted")
	}
}

type fakeMetadataRepo struct{ value storage.AgentRuntimeMetadata }

func (r *fakeMetadataRepo) Upsert(_ context.Context, v storage.AgentRuntimeMetadata) error {
	r.value = v
	return nil
}
func (r *fakeMetadataRepo) Get(context.Context, string) (storage.AgentRuntimeMetadata, error) {
	return r.value, nil
}
func (r *fakeMetadataRepo) List(context.Context, string, int) (storage.Page[storage.AgentRuntimeMetadata], error) {
	return storage.Page[storage.AgentRuntimeMetadata]{Items: []storage.AgentRuntimeMetadata{r.value}}, nil
}
func (r *fakeMetadataRepo) MarkStale(context.Context, string, int64) error {
	r.value.Stale = true
	return nil
}
