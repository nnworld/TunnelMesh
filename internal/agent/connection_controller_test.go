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
