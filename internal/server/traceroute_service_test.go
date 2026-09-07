package server

import (
	"context"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"testing"
)

func TestTracerouteServiceBuildsSignedMultiHopResult(t *testing.T) {
	fixture := newTestTracerouteService(t)
	result, err := fixture.Start(context.Background(), protocol.TraceRequest{TraceID: "tr-test", AgentID: "agent-a", MaxHops: 8}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hops) < 3 || !result.Completed {
		t.Fatalf("result = %+v", result)
	}
	if err := protocol.VerifyTraceHopChain(result.Hops, fixture.key); err != nil {
		t.Fatal(err)
	}
}

func newTestTracerouteService(t *testing.T) *TracerouteService {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:trace-service-"+t.Name()+"?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-a", Name: "agent-a", OwnerUserID: "u", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(context.Background(), storage.ServerNode{ID: "node-a", Address: "10.0.0.2:8443"}); err != nil {
		t.Fatal(err)
	}
	return NewTracerouteService(db)
}
