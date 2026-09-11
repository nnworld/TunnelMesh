package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type fakeClientConnectionRepo struct {
	registered []storage.ClientConnectionLease
	renewed    []string
	stats      []storage.ClientConnectionLease
	released   []string
	err        error
}

func (r *fakeClientConnectionRepo) Register(_ context.Context, lease storage.ClientConnectionLease) (storage.ClientConnectionLease, error) {
	r.registered = append(r.registered, lease)
	return lease, r.err
}
func (r *fakeClientConnectionRepo) Get(context.Context, string) (storage.ClientConnectionLease, error) {
	return storage.ClientConnectionLease{}, storage.ErrMetadataStale
}
func (r *fakeClientConnectionRepo) Renew(_ context.Context, connectionID string, _ int64, _ time.Duration) error {
	r.renewed = append(r.renewed, connectionID)
	return r.err
}
func (r *fakeClientConnectionRepo) Release(_ context.Context, connectionID string, _ int64) error {
	r.released = append(r.released, connectionID)
	return r.err
}
func (r *fakeClientConnectionRepo) List(context.Context, storage.ClientConnectionFilter, string, int) (storage.Page[storage.ClientConnectionLease], error) {
	return storage.Page[storage.ClientConnectionLease]{}, nil
}
func (r *fakeClientConnectionRepo) ListByInstance(context.Context, string) ([]storage.ClientConnectionLease, error) {
	return nil, nil
}
func (r *fakeClientConnectionRepo) ListByInstances(context.Context, []string) ([]storage.ClientConnectionLease, error) {
	return nil, nil
}
func (r *fakeClientConnectionRepo) UpdateStats(_ context.Context, lease storage.ClientConnectionLease) error {
	r.stats = append(r.stats, lease)
	return r.err
}

func TestClientConnectionLeaseControllerRegistersHeartbeatsAndReleases(t *testing.T) {
	repo := &fakeClientConnectionRepo{}
	manager := NewClientSessionManager()
	transport := newFakeTransport()
	record := ClientSessionRecord{
		ConnectionID: "connection-1", ClientInstanceID: "client-instance-1", TokenID: "token-1",
		OwnerUserID: "owner-1", ServerNodeID: "server-1", ConnectionEpoch: 7, StartedAt: time.Now().UTC(),
	}
	manager.Register(record, transport)
	controller := NewClientConnectionLeaseController(repo, manager, "server-1", 90*time.Second)

	if _, err := controller.Register(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(repo.registered) != 1 || repo.registered[0].ConnectionEpoch != 7 || repo.registered[0].ActiveStreams != 0 {
		t.Fatalf("registered lease = %#v", repo.registered)
	}

	manager.ObserveStreamOpened(record.ConnectionID)
	if err := controller.Heartbeat(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(repo.renewed) != 1 || repo.renewed[0] != record.ConnectionID {
		t.Fatalf("renewed = %v", repo.renewed)
	}
	if len(repo.stats) != 1 || repo.stats[0].ActiveStreams != 1 || repo.stats[0].HealthScore != 100 {
		t.Fatalf("stats = %#v", repo.stats)
	}

	if err := controller.Release(context.Background(), record.ConnectionID, record.ConnectionEpoch); err != nil {
		t.Fatal(err)
	}
	if len(repo.released) != 1 || repo.released[0] != record.ConnectionID {
		t.Fatalf("released = %v", repo.released)
	}
	if err := manager.CloseConnection(record.ConnectionID, record.ConnectionEpoch+1); !errors.Is(err, ErrEpoch) {
		t.Fatalf("stale close error = %v, want ErrEpoch", err)
	}
}
