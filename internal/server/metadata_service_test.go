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
	if _, err := svc.Upsert(context.Background(), AgentMetadataInput{AgentID: "a", NodeID: "n", Epoch: 1, Revision: 1, Items: []MetadataItem{{Name: "x", Source: "env", Value: strings.Repeat("x", 4<<10+1)}}}); err == nil {
		t.Fatal("oversized metadata field accepted")
	}
}

func TestAgentMetadataServiceListComputesExpiryStale(t *testing.T) {
	expired := time.Now().UTC().Add(-time.Minute)
	repo := &fakeMetadataRepo{list: storage.Page[storage.AgentRuntimeMetadata]{Items: []storage.AgentRuntimeMetadata{{AgentID: "a", Metadata: `{"items":[]}`, ExpiresAt: &expired}}}}
	page, err := NewAgentMetadataService(repo).List(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].Stale {
		t.Fatalf("page=%+v", page)
	}
}

func TestAgentMetadataServiceStoresInstancesIndependently(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:metadata-instances?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewAgentMetadataService(db.Metadata())
	ctx := context.Background()
	base := AgentMetadataInput{AgentID: "agent-instance", NodeID: "node-a", Epoch: 1, Revision: 1, Items: []MetadataItem{{Name: "region", Source: "env", Value: "a"}}}
	first := base
	first.InstanceID = "instance-a"
	if _, err := service.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := base
	second.InstanceID = "instance-b"
	second.NodeID = "node-b"
	second.Items = []MetadataItem{{Name: "region", Source: "env", Value: "b"}}
	if _, err := service.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}
	view, err := service.GetView(ctx, "agent-instance")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Instances) != 2 || view.Instances[0].InstanceID != "instance-a" || view.Instances[1].InstanceID != "instance-b" {
		t.Fatalf("instances=%+v, want two independent instances", view.Instances)
	}
	if err := service.MarkInstanceStale(ctx, "agent-instance", "instance-a", 1); err != nil {
		t.Fatal(err)
	}
	instances, err := service.ListInstances(ctx, "agent-instance")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 || !instances[0].Stale || instances[1].Stale {
		t.Fatalf("instances after stale=%+v, want only instance-a stale", instances)
	}
}

type fakeMetadataRepo struct {
	value storage.AgentRuntimeMetadata
	list  storage.Page[storage.AgentRuntimeMetadata]
}

func (r *fakeMetadataRepo) Upsert(_ context.Context, v storage.AgentRuntimeMetadata) error {
	r.value = v
	return nil
}
func (r *fakeMetadataRepo) Get(context.Context, string) (storage.AgentRuntimeMetadata, error) {
	return r.value, nil
}

func (r *fakeMetadataRepo) List(context.Context, string, int) (storage.Page[storage.AgentRuntimeMetadata], error) {
	if r.list.Items != nil {
		return r.list, nil
	}
	return storage.Page[storage.AgentRuntimeMetadata]{Items: []storage.AgentRuntimeMetadata{r.value}}, nil
}
func (r *fakeMetadataRepo) MarkStale(context.Context, string, int64) error {
	r.value.Stale = true
	return nil
}
func (r *fakeMetadataRepo) Touch(_ context.Context, _ string, _ int64, lastSeenAt, expiresAt time.Time) error {
	r.value.LastSeenAt = lastSeenAt
	r.value.ExpiresAt = &expiresAt
	r.value.Stale = false
	return nil
}
