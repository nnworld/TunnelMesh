package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// TestRealAgentRunsScriptThroughTheServerBroker connects a real agent that advertises
// agent_exec.v1, confirms the live session shows the capability, then runs a script
// through the server's exec broker and checks the browser-side frames.
func TestRealAgentRunsScriptThroughTheServerBroker(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:agent-exec-e2e?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := auth.NewAuthService(db).CreateUser(ctx, "exec-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	agentID := "exec-agent"
	if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	token, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	listen := freeLoopbackAddr(t)
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		AgentTransports: config.ServerAgentTransportsConfig{TCP: config.ServerAgentListenerConfig{Listen: listen}},
		Exec:            config.ServerExecConfig{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = runtime.ServeListener(serveCtx, httpListener) }()

	agentCtx, agentCancel := context.WithCancel(ctx)
	defer agentCancel()
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- agent.RunConnectionPool(agentCtx, agent.WebSocketPoolOptions{
			ServerURL: "tcp://" + listen, Token: token.Secret, AgentID: agentID, NodeID: "node-exec", Epoch: 1,
			Collector: agent.NewMetadataCollector(nil),
			Factory:   agent.NewStreamDispatcherFactoryWithExec(config.AgentStreamConfig{}, nil, agent.ExecConfig{Enabled: true}),
			Min:       1, Max: 1, HighWatermark: 1, LowWatermark: 0,
			EvaluationInterval: time.Second, Cooldown: time.Second,
		})
	}()

	// Wait for a live session that negotiated the capability.
	deadline := time.Now().Add(8 * time.Second)
	for {
		if liveSessionsSupportExec(runtime.AgentSessions, agentID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never advertised agent_exec.v1 on a live session")
		}
		time.Sleep(20 * time.Millisecond)
	}

	created, err := runtime.agentExec.tickets.issue(agentID, owner.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ws := &fakeExecWS{incoming: [][]byte{[]byte(`{"type":"run","script":"echo hi-from-agent; exit 4","timeoutSec":20}`)}}
	if err := runtime.agentExec.Handle(ctx, ws, created.SessionID, created.Ticket); err != nil {
		t.Fatalf("handle: %v", err)
	}
	var stdout string
	var exitCode any
	for _, frame := range ws.sent {
		switch frame["type"] {
		case "stdout":
			stdout += decodeFrameData(t, frame["data"])
		case "exit":
			exitCode = frame["exitCode"]
		}
	}
	if !strings.Contains(stdout, "hi-from-agent") || exitCode != float64(4) {
		t.Fatalf("stdout=%q exit=%v frames=%+v", stdout, exitCode, ws.sent)
	}

	// The ticket is spent: a second use must not start another run.
	replay := &fakeExecWS{incoming: [][]byte{[]byte(`{"type":"run","script":"echo again","timeoutSec":5}`)}}
	if err := runtime.agentExec.Handle(ctx, replay, created.SessionID, created.Ticket); err == nil {
		t.Fatal("a spent ticket must not run again")
	}
	agentCancel()
	<-agentDone
}
