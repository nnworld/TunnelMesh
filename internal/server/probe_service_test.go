package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type probeExec struct{}

func (probeExec) Execute(_ context.Context, req DiagnoseRequest) ProbeResult {
	return ProbeResult{Kind: req.Kind, Result: "success"}
}

func TestProbeServiceOwnerAdminBoundaryAndPersistence(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:probe-service?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-1", Name: "a", OwnerUserID: "owner", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Leases().Acquire(context.Background(), storage.AgentLease{AgentID: "agent-1", NodeID: "node-1", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	svc := NewProbeService(db, probeExec{})
	req := DiagnoseRequest{Kind: "tcp", Host: "127.0.0.1", Port: 80}
	if _, err := svc.Diagnose(context.Background(), auth.Principal{UserID: "other", Role: "user"}, "agent-1", req); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("err=%v, want forbidden", err)
	}
	result, err := svc.Diagnose(context.Background(), auth.Principal{UserID: "owner", Role: "user"}, "agent-1", req)
	if err != nil || result.Result != "success" || result.ProbeID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	page, err := db.ProbeResults().ListByAgent(context.Background(), "agent-1", "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ProbeID != result.ProbeID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := svc.Diagnose(context.Background(), auth.Principal{UserID: "admin", Role: "admin"}, "agent-1", req); err != nil {
		t.Fatalf("admin diagnose err=%v", err)
	}
}

func TestProbeServiceRejectsUnboundedRequest(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:probe-service-invalid?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-2", Name: "a", OwnerUserID: "owner", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Leases().Acquire(context.Background(), storage.AgentLease{AgentID: "agent-2", NodeID: "node-1", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	svc := NewProbeService(db)
	if _, err := svc.Diagnose(context.Background(), auth.Principal{UserID: "owner"}, "agent-2", DiagnoseRequest{Kind: "tcp", Host: "x", Port: 1, Timeout: 31 * 1e9}); err == nil {
		t.Fatal("expected timeout validation error")
	}
}
