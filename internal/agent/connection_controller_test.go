package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type recordingConnectionRunner struct {
	mu       sync.Mutex
	started  []string
	failOnce map[string]error
}

func newRecordingConnectionRunner() *recordingConnectionRunner {
	return &recordingConnectionRunner{failOnce: make(map[string]error)}
}

func (r *recordingConnectionRunner) Run(ctx context.Context, connectionID string, _ int64) error {
	r.mu.Lock()
	r.started = append(r.started, connectionID)
	fail := r.failOnce[connectionID]
	delete(r.failOnce, connectionID)
	r.mu.Unlock()
	if fail != nil {
		return fail
	}
	<-ctx.Done()
	return ctx.Err()
}

func (r *recordingConnectionRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started)
}

func waitActiveConnections(t *testing.T, controller *ConnectionController, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if controller.Snapshot().Active == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("active connections=%d, want %d", controller.Snapshot().Active, want)
}

func TestConnectionControllerScalesWithHysteresis(t *testing.T) {
	runner := newRecordingConnectionRunner()
	metricsRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(metricsRegistry)
	controller := NewConnectionController(ConnectionControllerOptions{
		Min: 1, Max: 2, HighWatermark: 2, LowWatermark: 0,
		EvaluationInterval: 2 * time.Millisecond, Cooldown: 4 * time.Millisecond,
		HighRTT: time.Millisecond, Runner: runner.Run, AgentID: "metrics-agent", InstanceID: "instance-1", Metrics: metrics,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("controller did not stop")
		}
	}()
	waitActiveConnections(t, controller, 1)

	controller.Report("conn-1", ConnectionStats{ConnectionPoolSupported: true, ActiveStreams: 2})
	waitActiveConnections(t, controller, 2)
	if active := controller.Snapshot().Active; active > 2 {
		t.Fatalf("active connections=%d, want max 2", active)
	}

	controller.Report("conn-1", ConnectionStats{ConnectionPoolSupported: true, ActiveStreams: 0})
	controller.Report("conn-2", ConnectionStats{ConnectionPoolSupported: true, ActiveStreams: 0})
	waitActiveConnections(t, controller, 1)
	families, err := metricsRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var exposition strings.Builder
	for _, family := range families {
		exposition.WriteString(family.GetName())
	}
	if !strings.Contains(exposition.String(), "tunnelmesh_agent_connection_scale_decisions_total") {
		t.Fatalf("scale decision metric not published: %s", exposition.String())
	}
}

func TestConnectionControllerRequiresServerAckAndRestartsFailedConnection(t *testing.T) {
	runner := newRecordingConnectionRunner()
	runner.failOnce["conn-1"] = errors.New("dial failed")
	controller := NewConnectionController(ConnectionControllerOptions{
		Min: 1, Max: 2, HighWatermark: 1, LowWatermark: 0,
		EvaluationInterval: 2 * time.Millisecond, Cooldown: 4 * time.Millisecond,
		Runner: runner.Run,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("controller did not stop")
		}
	}()
	waitActiveConnections(t, controller, 1)
	controller.Report("conn-1", ConnectionStats{ActiveStreams: 9, QueuePressure: 100, RTT: time.Second})
	time.Sleep(20 * time.Millisecond)
	if active := controller.Snapshot().Active; active != 1 {
		t.Fatalf("active connections=%d, want 1 without Server ack", active)
	}
	waitActiveConnections(t, controller, 1)
	if runner.count() < 2 {
		t.Fatalf("connection restarts=%d, want at least 2", runner.count())
	}
}

func TestConnectionControllerTracksLatencySignals(t *testing.T) {
	controller := NewConnectionController(ConnectionControllerOptions{Runner: func(context.Context, string, int64) error { return nil }})
	controller.Report("conn-1", ConnectionStats{
		PendingDials: 2, OpenP95: 150 * time.Millisecond, TTFBP95: 220 * time.Millisecond, WriterQueueWait: 30 * time.Millisecond,
	})
	snapshot := controller.Snapshot()
	if snapshot.MaxPendingDials != 2 || snapshot.MaxOpenP95 != 150*time.Millisecond ||
		snapshot.MaxTTFBP95 != 220*time.Millisecond || snapshot.MaxWriterQueueWait != 30*time.Millisecond {
		t.Fatalf("snapshot=%+v, want all latency signals", snapshot)
	}
}

type latencyStatsHandler struct {
	pendingDials    int
	openP95         time.Duration
	ttfbP95         time.Duration
	writerQueueWait time.Duration
}

func (h *latencyStatsHandler) Handle(protocol.Frame) error       { return nil }
func (h *latencyStatsHandler) Close() error                      { return nil }
func (h *latencyStatsHandler) PendingDials() int                 { return h.pendingDials }
func (h *latencyStatsHandler) OpenP95() time.Duration            { return h.openP95 }
func (h *latencyStatsHandler) TTFBP95() time.Duration            { return h.ttfbP95 }
func (h *latencyStatsHandler) WriterQueueWaitP95() time.Duration { return h.writerQueueWait }

type dispatcherLatencyStatsHandler struct {
	pendingDials int
	openP95      time.Duration
	ttfbP95      time.Duration
}

func (h *dispatcherLatencyStatsHandler) Handle(protocol.Frame) error { return nil }
func (h *dispatcherLatencyStatsHandler) Close() error                { return nil }
func (h *dispatcherLatencyStatsHandler) PendingDials() int           { return h.pendingDials }
func (h *dispatcherLatencyStatsHandler) OpenP95() time.Duration      { return h.openP95 }
func (h *dispatcherLatencyStatsHandler) TTFBP95() time.Duration      { return h.ttfbP95 }

func TestConnectionPoolReportsLatencySignals(t *testing.T) {
	controller := NewConnectionController(ConnectionControllerOptions{Runner: func(context.Context, string, int64) error { return nil }})
	session := NewSession(&blockingTransport{closed: make(chan struct{})})
	defer session.Close()
	handler := &connectionPoolHandler{
		controller: controller, connectionID: "conn-latency", session: session,
		inner: &latencyStatsHandler{pendingDials: 1, openP95: 40 * time.Millisecond, ttfbP95: 60 * time.Millisecond, writerQueueWait: 7 * time.Millisecond},
	}
	handler.report(0)
	snapshot := controller.Snapshot()
	if snapshot.MaxPendingDials != 1 || snapshot.MaxOpenP95 != 40*time.Millisecond ||
		snapshot.MaxTTFBP95 != 60*time.Millisecond || snapshot.MaxWriterQueueWait != 7*time.Millisecond {
		t.Fatalf("snapshot=%+v, want dispatcher latency signals", snapshot)
	}
}

func TestConnectionPoolReportsSessionWriterQueueWait(t *testing.T) {
	controller := NewConnectionController(ConnectionControllerOptions{Runner: func(context.Context, string, int64) error { return nil }})
	transport := newBlockingAgentFrameTransport()
	session := NewSession(transport)
	defer func() {
		close(transport.release)
		_ = session.Close()
		controller.closeAll()
		controller.wg.Wait()
	}()
	handler := &connectionPoolHandler{
		controller: controller, connectionID: "conn-writer", session: session,
		inner: &dispatcherLatencyStatsHandler{pendingDials: 1, openP95: 40 * time.Millisecond, ttfbP95: 60 * time.Millisecond},
	}

	for i := 0; i < 2; i++ {
		if err := session.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: uint32(i + 1), Payload: []byte("queued")}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for session.WriterQueueWaitP95() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	handler.report(0)

	snapshot := controller.Snapshot()
	if snapshot.MaxWriterQueueWait <= 0 {
		t.Fatalf("snapshot=%+v, want writer queue wait from Session", snapshot)
	}
}

func TestConnectionControllerScalesOnLatencySignals(t *testing.T) {
	controller := NewConnectionController(ConnectionControllerOptions{
		Min: 1, Max: 2, HighWatermark: 16, LowWatermark: 2,
		Runner: func(ctx context.Context, _ string, _ int64) error {
			<-ctx.Done()
			return context.Canceled
		},
	})
	defer func() {
		controller.closeAll()
		controller.wg.Wait()
	}()
	controller.mu.Lock()
	controller.startLocked(context.Background())
	controller.mu.Unlock()
	controller.Report("conn-1", ConnectionStats{
		ConnectionPoolSupported: true,
		PendingDials:            1,
		OpenP95:                 2 * time.Second,
		TTFBP95:                 2 * time.Second,
		WriterQueueWait:         200 * time.Millisecond,
	})
	controller.evaluate(time.Time{})
	controller.evaluate(time.Time{})
	if active := controller.Snapshot().Active; active != 2 {
		t.Fatalf("active connections=%d, want 2 after sustained latency load", active)
	}
}
