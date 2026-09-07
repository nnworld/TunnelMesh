package observability_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestAgentDialAndStorageFailuresAreInstrumented(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = agent.RunWebSocketWithOptions(ctx, "ws://127.0.0.1:1/ws", "token", "agent-1", "node-1", 1, nil, nil, agent.WebSocketRunOptions{
		BaseBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
		Metrics:     metrics,
	})

	db, err := storage.OpenWithMetrics(context.Background(), storage.DriverSQLite, ":memory:", true, metrics)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = db.Close()
	_ = db.Ping(context.Background())

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var exposition strings.Builder
	for _, family := range families {
		exposition.WriteString(family.GetName())
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				exposition.WriteByte(' ')
				exposition.WriteString(label.GetName())
				exposition.WriteByte('=')
				exposition.WriteString(label.GetValue())
			}
		}
		exposition.WriteByte('\n')
	}
	text := exposition.String()
	if !strings.Contains(text, "tunnelmesh_connections_total") || !strings.Contains(text, "tunnelmesh_connection_stage_duration_seconds") {
		t.Fatalf("agent failure metric missing: %s", text)
	}
	if !strings.Contains(text, "tunnelmesh_probe_total") || !strings.Contains(text, "storage") {
		t.Fatalf("storage probe metric missing: %s", text)
	}
}
