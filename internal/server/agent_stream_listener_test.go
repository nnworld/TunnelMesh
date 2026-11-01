package server

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// agentStreamFixture serves a raw listener with a fake authenticator in place of the
// database-backed one, so the preface and budget rules can be exercised directly.
func agentStreamFixture(t *testing.T, accept func(token string) (agentConnectionAuthentication, error), limits agentStreamLimits) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	authenticate := func(_ context.Context, token string) (agentConnectionAuthentication, error) {
		return accept(token)
	}
	listener := newAgentStreamListener("tcp", nil, limits, authenticate, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = listener.Serve(ctx, ln) }()
	return ln.Addr().String(), func() {
		cancel()
		_ = listener.Close()
		<-done
	}
}

func okAuth(string) (agentConnectionAuthentication, error) {
	return agentConnectionAuthentication{}, nil
}

func replyStatus(t *testing.T, conn net.Conn) protocol.PrefaceStatus {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var reply [2]byte
	n := 0
	for n < 2 {
		m, err := conn.Read(reply[n:])
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		n += m
	}
	return protocol.PrefaceStatus(reply[0])
}

func TestAgentStreamRejectsWrongTokenBeforeAnyFrame(t *testing.T) {
	addr, stop := agentStreamFixture(t, func(string) (agentConnectionAuthentication, error) {
		return agentConnectionAuthentication{}, errAgentStreamTest
	}, agentStreamLimits{})
	defer stop()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := protocol.WritePreface(conn, []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	if status := replyStatus(t, conn); status != protocol.StatusUnauthorized {
		t.Fatalf("status = %s, want unauthorized", status)
	}
}

func TestAgentStreamAcceptsValidPrefaceAndReportsOK(t *testing.T) {
	addr, stop := agentStreamFixture(t, okAuth, agentStreamLimits{})
	defer stop()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := protocol.WritePreface(conn, []byte("good")); err != nil {
		t.Fatal(err)
	}
	if status := replyStatus(t, conn); status != protocol.StatusOK {
		t.Fatalf("status = %s, want ok", status)
	}
}

func TestAgentStreamRefusesMalformedPreface(t *testing.T) {
	addr, stop := agentStreamFixture(t, okAuth, agentStreamLimits{})
	defer stop()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(bytes.Repeat([]byte("X"), 16)); err != nil {
		t.Fatal(err)
	}
	if status := replyStatus(t, conn); status != protocol.StatusBadPreface {
		t.Fatalf("status = %s, want bad_preface", status)
	}
}

func TestAgentStreamClosesSlowPrefaceAtTheDeadline(t *testing.T) {
	addr, stop := agentStreamFixture(t, okAuth, agentStreamLimits{PrefaceTimeout: 100 * time.Millisecond})
	defer stop()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	// The server answers the missed deadline with bad_preface, then closes.
	if status := replyStatus(t, conn); status != protocol.StatusBadPreface {
		t.Fatalf("status = %s, want bad_preface at the deadline", status)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("the connection must close after the refusal")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("preface deadline took %s", elapsed)
	}
}

func TestAgentStreamBudgetCapsUnauthenticatedConnectionsPerIP(t *testing.T) {
	budget := newAgentStreamBudget(agentStreamLimits{MaxUnauthTotal: 10, MaxUnauthPerIP: 2})
	first, ok := budget.acquire("10.0.0.1")
	if !ok {
		t.Fatal("first connection must be admitted")
	}
	second, ok := budget.acquire("10.0.0.1")
	if !ok {
		t.Fatal("second connection from the same IP is within the cap")
	}
	if _, ok := budget.acquire("10.0.0.1"); ok {
		t.Fatal("a third unauthenticated connection from one IP must be refused")
	}
	first()
	first() // releasing twice must not free a second slot
	if _, ok := budget.acquire("10.0.0.1"); !ok {
		t.Fatal("releasing a slot must make room again")
	}
	second()
}

func TestAgentStreamBudgetCapsTotalAcrossIPs(t *testing.T) {
	budget := newAgentStreamBudget(agentStreamLimits{MaxUnauthTotal: 2, MaxUnauthPerIP: 8})
	budget.acquire("10.0.0.1")
	budget.acquire("10.0.0.2")
	if _, ok := budget.acquire("10.0.0.3"); ok {
		t.Fatal("the global unauthenticated cap must hold across addresses")
	}
}

func TestAgentStreamBudgetBlocksRepeatedAuthFailures(t *testing.T) {
	budget := newAgentStreamBudget(agentStreamLimits{MaxUnauthTotal: 100, MaxUnauthPerIP: 100})
	for i := 0; i < agentStreamMaxFailures; i++ {
		budget.recordFailure("10.9.9.9")
	}
	if _, ok := budget.acquire("10.9.9.9"); ok {
		t.Fatal("an address with repeated failed authentications must be refused")
	}
	if _, ok := budget.acquire("10.9.9.8"); !ok {
		t.Fatal("failures must not penalise other addresses")
	}
}

var errAgentStreamTest = errorString("agent stream test: refused")

type errorString string

func (e errorString) Error() string { return string(e) }
