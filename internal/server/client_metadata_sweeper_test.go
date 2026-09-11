package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type fakeClientInstanceRepo struct {
	mu         sync.Mutex
	upserts    []storage.ClientInstance
	touched    [][2]time.Time
	touchedIDs [][2]string
	markStale  []string
	expired    []time.Time
	nextID     string
	err        error
}

func (r *fakeClientInstanceRepo) Upsert(_ context.Context, instance storage.ClientInstance) (storage.ClientInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upserts = append(r.upserts, instance)
	if instance.ID == "" {
		instance.ID = r.nextID
	}
	return instance, r.err
}
func (r *fakeClientInstanceRepo) GetByOwnerAndInstance(context.Context, string, string) (storage.ClientInstance, error) {
	return storage.ClientInstance{}, storage.ErrMetadataStale
}
func (r *fakeClientInstanceRepo) Get(context.Context, string) (storage.ClientInstance, error) {
	return storage.ClientInstance{}, storage.ErrMetadataStale
}
func (r *fakeClientInstanceRepo) List(context.Context, storage.ClientInstanceFilter, string, int) (storage.Page[storage.ClientInstance], error) {
	return storage.Page[storage.ClientInstance]{}, nil
}
func (r *fakeClientInstanceRepo) TouchInstance(_ context.Context, ownerUserID, instanceID string, lastSeenAt, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchedIDs = append(r.touchedIDs, [2]string{ownerUserID, instanceID})
	r.touched = append(r.touched, [2]time.Time{lastSeenAt, expiresAt})
	return r.err
}
func (r *fakeClientInstanceRepo) MarkStale(_ context.Context, id string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markStale = append(r.markStale, id)
	return r.err
}
func (r *fakeClientInstanceRepo) MarkExpired(_ context.Context, at time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expired = append(r.expired, at)
	return int64(len(r.expired)), r.err
}

func TestClientMetadataSweeperMarksExpiredAndStops(t *testing.T) {
	repo := &fakeClientInstanceRepo{}
	sweeper := NewClientMetadataSweeper(repo, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()
	deadline := time.After(time.Second)
	for {
		repo.mu.Lock()
		expired := len(repo.expired)
		repo.mu.Unlock()
		if expired > 0 {
			cancel()
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("sweeper did not mark expired metadata")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sweeper did not stop")
	}
}
