package server

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type fakeClientConnectionRepo struct {
	registered  []storage.ClientConnectionLease
	renewed     []string
	renewEpochs []int64
	stats       []storage.ClientConnectionLease
	released    []string
	err         error
	// renewErr overrides err for Renew only, so a test can model "the epoch
	// predicate matched no row" while Register still succeeds.
	renewErr error
}

func (r *fakeClientConnectionRepo) Register(_ context.Context, lease storage.ClientConnectionLease) (storage.ClientConnectionLease, error) {
	r.registered = append(r.registered, lease)
	return lease, r.err
}
func (r *fakeClientConnectionRepo) Get(context.Context, string) (storage.ClientConnectionLease, error) {
	return storage.ClientConnectionLease{}, storage.ErrMetadataStale
}
func (r *fakeClientConnectionRepo) Renew(_ context.Context, connectionID string, epoch int64, _ time.Duration) error {
	r.renewed = append(r.renewed, connectionID)
	r.renewEpochs = append(r.renewEpochs, epoch)
	if r.renewErr != nil {
		return r.renewErr
	}
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

// TestClientConnectionLeaseHeartbeatSelfHealsEpochMismatch covers the recovery
// path for leases whose stored connection_epoch no longer matches the
// authoritative in-memory token. On MySQL a 32-bit column clamped the random
// int64 epoch to 2147483647, so Renew matched zero rows and every heartbeat
// failed for the lifetime of the connection. The in-memory ClientSessionManager
// owns the true epoch, so a missed renewal re-registers the lease instead of
// giving up; that repairs already-connected clients on the next heartbeat
// without forcing a reconnect.
func TestClientConnectionLeaseHeartbeatSelfHealsEpochMismatch(t *testing.T) {
	repo := &fakeClientConnectionRepo{renewErr: sql.ErrNoRows}
	manager := NewClientSessionManager()
	record := ClientSessionRecord{
		ConnectionID: "connection-heal", ClientInstanceID: "client-instance-1", TokenID: "token-1",
		OwnerUserID: "owner-1", ServerNodeID: "server-1",
		ConnectionEpoch: int64(math.MaxInt32) + 99, StartedAt: time.Now().UTC(),
	}
	manager.Register(record, newFakeTransport())
	controller := NewClientConnectionLeaseController(repo, manager, "server-1", 90*time.Second)

	if err := controller.Heartbeat(context.Background(), record); err != nil {
		t.Fatalf("heartbeat should self-heal an epoch mismatch, got %v", err)
	}
	if len(repo.renewed) != 1 || repo.renewEpochs[0] != record.ConnectionEpoch {
		t.Fatalf("renew calls = %v epochs = %v, want one attempt with %d", repo.renewed, repo.renewEpochs, record.ConnectionEpoch)
	}
	if len(repo.registered) != 1 {
		t.Fatalf("expected one re-registration, got %#v", repo.registered)
	}
	healed := repo.registered[0]
	if healed.ConnectionEpoch != record.ConnectionEpoch {
		t.Fatalf("re-registered epoch = %d, want %d", healed.ConnectionEpoch, record.ConnectionEpoch)
	}
	if healed.ConnectionID != record.ConnectionID || healed.ClientInstanceID != record.ClientInstanceID {
		t.Fatalf("re-registered lease identity = %#v", healed)
	}
	if healed.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("re-registered lease must not already be expired: %#v", healed)
	}
}

// A renewal miss caused by something other than a stale epoch — for example a
// dropped database connection — must still surface, otherwise the self-heal
// path would mask real storage failures behind a silent re-register.
func TestClientConnectionLeaseHeartbeatPropagatesNonEpochErrors(t *testing.T) {
	repo := &fakeClientConnectionRepo{renewErr: errors.New("connection reset")}
	manager := NewClientSessionManager()
	record := ClientSessionRecord{
		ConnectionID: "connection-broken", ClientInstanceID: "client-instance-1", TokenID: "token-1",
		OwnerUserID: "owner-1", ServerNodeID: "server-1", ConnectionEpoch: 11, StartedAt: time.Now().UTC(),
	}
	manager.Register(record, newFakeTransport())
	controller := NewClientConnectionLeaseController(repo, manager, "server-1", 90*time.Second)

	err := controller.Heartbeat(context.Background(), record)
	if err == nil || err.Error() != "connection reset" {
		t.Fatalf("heartbeat error = %v, want the underlying storage failure", err)
	}
	if len(repo.registered) != 0 {
		t.Fatalf("must not re-register on a transport failure: %#v", repo.registered)
	}
}

// A lease row must always identify a known client instance. Before CLIENT_HELLO
// there is nothing to heal yet, so the self-heal must not create an orphan row
// that no instance-scoped query could ever attribute.
func TestClientConnectionLeaseHeartbeatDoesNotRegisterWithoutInstanceID(t *testing.T) {
	repo := &fakeClientConnectionRepo{renewErr: sql.ErrNoRows}
	manager := NewClientSessionManager()
	record := ClientSessionRecord{
		ConnectionID: "connection-no-instance", TokenID: "token-1", OwnerUserID: "owner-1",
		ServerNodeID: "server-1", ConnectionEpoch: 5, StartedAt: time.Now().UTC(),
	}
	manager.Register(record, newFakeTransport())
	controller := NewClientConnectionLeaseController(repo, manager, "server-1", 90*time.Second)

	if err := controller.Heartbeat(context.Background(), record); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("heartbeat error = %v, want sql.ErrNoRows", err)
	}
	if len(repo.registered) != 0 {
		t.Fatalf("must not register a lease without an instance id: %#v", repo.registered)
	}
}
