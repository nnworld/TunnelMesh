package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestWebSSHSweeperExpiresPendingAndActiveSessions(t *testing.T) {
	ctx := context.Background()
	db := webSSHSweeperFixture(t)
	now := time.Now().UTC()
	createWebSSHSweeperSession(t, db, "pending-expired", storage.WebSSHSessionPending, now.Add(-time.Second), now.Add(time.Hour))
	createWebSSHSweeperSession(t, db, "active-expired", storage.WebSSHSessionActive, now.Add(time.Minute), now.Add(-time.Second))
	createWebSSHSweeperSession(t, db, "active-valid", storage.WebSSHSessionActive, now.Add(time.Minute), now.Add(time.Hour))

	sweeper := NewWebSSHSweeper(db.WebSSHSessions(), time.Millisecond)
	changed, err := sweeper.Sweep(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 2 {
		t.Fatalf("changed sessions = %d, want 2", changed)
	}
	assertWebSSHSweeperStatus(t, db, "pending-expired", storage.WebSSHSessionExpired)
	assertWebSSHSweeperStatus(t, db, "active-expired", storage.WebSSHSessionExpired)
	assertWebSSHSweeperStatus(t, db, "active-valid", storage.WebSSHSessionActive)
}

func TestWebSSHSweeperRunsUntilCancel(t *testing.T) {
	db := webSSHSweeperFixture(t)
	now := time.Now().UTC()
	createWebSSHSweeperSession(t, db, "pending-expired", storage.WebSSHSessionPending, now.Add(-time.Second), now.Add(time.Hour))
	sweeper := NewWebSSHSweeper(db.WebSSHSessions(), time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()
	deadline := time.After(time.Second)
	for {
		session, err := db.WebSSHSessions().Get(context.Background(), "pending-expired")
		if err != nil {
			t.Fatal(err)
		}
		if session.Status == storage.WebSSHSessionExpired {
			cancel()
			break
		}
		select {
		case <-deadline:
			t.Fatal("sweeper did not expire pending ticket")
		case <-time.After(time.Millisecond):
		}
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServerRuntimeCloseStopsWebSSHSweeper(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-webssh-sweeper?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{WebSSH: config.WebSSHConfig{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.websshSweeper == nil || runtime.websshSweeperCancel == nil || runtime.websshSweeperDone == nil {
		t.Fatal("WebSSH sweeper was not started")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.websshSweeperCancel != nil || runtime.websshSweeperDone != nil {
		t.Fatal("WebSSH sweeper was not stopped by Close")
	}
}

func webSSHSweeperFixture(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), "file:webssh-sweeper-"+t.Name()+"?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createWebSSHSweeperSession(t *testing.T, db *storage.DB, id string, status storage.WebSSHSessionStatus, ticketExpires, expires time.Time) {
	t.Helper()
	_, err := db.WebSSHSessions().Create(context.Background(), storage.WebSSHSession{
		ID: id, OwnerUserID: "user-a", RemoteServerID: "server-a", AgentID: "agent-a", OwnerNodeID: "node-a",
		TicketHash: "hash-" + id, TicketExpiresAt: ticketExpires, Status: status,
		CreatedAt: time.Now().UTC(), ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertWebSSHSweeperStatus(t *testing.T, db *storage.DB, id string, want storage.WebSSHSessionStatus) {
	t.Helper()
	session, err := db.WebSSHSessions().Get(context.Background(), id)
	if err != nil || session.Status != want {
		t.Fatalf("session %s = %+v err=%v, want status %s", id, session, err, want)
	}
}

// failingWebSSHRepo is a two-method double: like recordingClientConnectionRepo
// it embeds the interface so any new repository call the sweeper makes panics in
// tests instead of quietly succeeding.
type failingWebSSHRepo struct {
	storage.WebSSHSessionRepository
	mu        sync.Mutex
	rounds    int
	err       error
	expiredAt []time.Time
}

func (r *failingWebSSHRepo) ExpirePending(_ context.Context, at time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds++
	r.expiredAt = append(r.expiredAt, at)
	return 0, r.err
}

func (r *failingWebSSHRepo) CloseExpiredActive(_ context.Context, _ time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return 0, r.err
}

func (r *failingWebSSHRepo) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *failingWebSSHRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rounds
}

// Pending WebSSH tickets that nothing expires become reusable-looking tickets
// and sessions that nothing closes keep an Agent stream alive. One unreachable
// query must therefore cost a round, not the job.
func TestWebSSHSweeperSurvivesFailedRounds(t *testing.T) {
	repo := &failingWebSSHRepo{err: errors.New("database is not reachable")}
	sweeper := NewWebSSHSweeper(repo, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for repo.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	repo.setErr(nil)
	for repo.count() < 5 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled after %d rounds", err, repo.count())
	}
	if got := repo.count(); got < 5 {
		t.Fatalf("rounds = %d, want the sweeper to keep ticking past a failed round", got)
	}
}
