package observability

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics owns the bounded Prometheus collectors used by runtime observers.
// A registry is injected so tests and embedded users never mutate the global registry.
type Metrics struct {
	connectionsTotal        *prometheus.CounterVec
	connectionsActive       *prometheus.GaugeVec
	connectionStageDuration *prometheus.HistogramVec
	heartbeatTotal          *prometheus.CounterVec
	heartbeatRTT            *prometheus.HistogramVec
	bytesTotal              *prometheus.CounterVec
	streamsActive           *prometheus.GaugeVec
	streamsTotal            *prometheus.CounterVec
	streamErrorsTotal       *prometheus.CounterVec
	probeTotal              *prometheus.CounterVec
	probeDuration           *prometheus.HistogramVec
	ready                   *prometheus.GaugeVec
}

// NewMetrics creates and registers a complete metrics set. Nil creates an isolated registry.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &Metrics{
		connectionsTotal:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_connections_total", Help: "Total connection attempts by result."}, []string{"component", "mode", "result", "error_class"}),
		connectionsActive:       prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_connections_active", Help: "Current active connections."}, []string{"component", "mode"}),
		connectionStageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_connection_stage_duration_seconds", Help: "Connection stage duration in seconds."}, []string{"component", "stage", "result", "error_class"}),
		heartbeatTotal:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_heartbeat_total", Help: "Total heartbeat outcomes."}, []string{"component", "result"}),
		heartbeatRTT:            prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_heartbeat_rtt_seconds", Help: "Heartbeat round-trip time in seconds."}, []string{"component"}),
		bytesTotal:              prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_bytes_total", Help: "Total bytes transferred."}, []string{"component", "direction", "protocol"}),
		streamsActive:           prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_streams_active", Help: "Current active logical streams."}, []string{"protocol"}),
		streamsTotal:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_streams_total", Help: "Total logical stream outcomes."}, []string{"protocol", "result", "error_class"}),
		streamErrorsTotal:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_stream_errors_total", Help: "Total logical stream errors."}, []string{"protocol", "error_class"}),
		probeTotal:              prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_probe_total", Help: "Total probe outcomes."}, []string{"probe_kind", "result", "error_class"}),
		probeDuration:           prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_probe_duration_seconds", Help: "Probe duration in seconds."}, []string{"probe_kind", "result", "error_class"}),
		ready:                   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_ready", Help: "Readiness state, one when ready."}, []string{"component"}),
	}
	reg.MustRegister(m.connectionsTotal, m.connectionsActive, m.connectionStageDuration, m.heartbeatTotal, m.heartbeatRTT, m.bytesTotal, m.streamsActive, m.streamsTotal, m.streamErrorsTotal, m.probeTotal, m.probeDuration, m.ready)
	return m
}

func label(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 64 {
		value = value[:64]
	}
	for i, r := range value {
		if r < 0x20 || r == 0x7f {
			value = value[:i] + "_" + value[i+1:]
		}
	}
	return value
}

func (m *Metrics) ObserveConnection(component, mode, result, errorClass string) {
	component, mode, result, errorClass = label(component), label(mode), label(result), label(errorClass)
	m.connectionsTotal.WithLabelValues(component, mode, result, errorClass).Inc()
	switch result {
	case "started", "active", "connected":
		m.connectionsActive.WithLabelValues(component, mode).Inc()
	case "closed", "disconnected", "failed", "error":
		m.connectionsActive.WithLabelValues(component, mode).Dec()
	}
}

func (m *Metrics) ObserveStage(component, stage, result, errorClass string, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.connectionStageDuration.WithLabelValues(label(component), label(stage), label(result), label(errorClass)).Observe(duration.Seconds())
}

func (m *Metrics) ObserveHeartbeat(component, result string, rtt time.Duration) {
	m.heartbeatTotal.WithLabelValues(label(component), label(result)).Inc()
	if rtt >= 0 {
		m.heartbeatRTT.WithLabelValues(label(component)).Observe(rtt.Seconds())
	}
}

func (m *Metrics) ObserveBytes(component, direction, protocol string, n int64) {
	if n > 0 {
		m.bytesTotal.WithLabelValues(label(component), label(direction), label(protocol)).Add(float64(n))
	}
}

func (m *Metrics) ObserveStream(protocol, result, errorClass string) {
	protocol, result, errorClass = label(protocol), label(result), label(errorClass)
	m.streamsTotal.WithLabelValues(protocol, result, errorClass).Inc()
	if errorClass != "" {
		m.streamErrorsTotal.WithLabelValues(protocol, errorClass).Inc()
	}
	switch result {
	case "accepted", "started", "active", "open":
		m.streamsActive.WithLabelValues(protocol).Inc()
	case "closed", "reset", "rejected", "failed":
		m.streamsActive.WithLabelValues(protocol).Dec()
	}
}

func (m *Metrics) ObserveProbe(kind, result, errorClass string, duration time.Duration) {
	kind, result, errorClass = label(kind), label(result), label(errorClass)
	m.probeTotal.WithLabelValues(kind, result, errorClass).Inc()
	if duration >= 0 {
		m.probeDuration.WithLabelValues(kind, result, errorClass).Observe(duration.Seconds())
	}
}

func (m *Metrics) SetReady(component string, ready bool) {
	value := float64(0)
	if ready {
		value = 1
	}
	m.ready.WithLabelValues(label(component)).Set(value)
}
