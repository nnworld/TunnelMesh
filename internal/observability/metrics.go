package observability

import (
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics owns the bounded Prometheus collectors used by runtime observers.
// A registry is injected so tests and embedded users never mutate the global registry.
type Metrics struct {
	connectionsTotal          *prometheus.CounterVec
	connectionsActive         *prometheus.GaugeVec
	connectionStageDuration   *prometheus.HistogramVec
	heartbeatTotal            *prometheus.CounterVec
	heartbeatRTT              *prometheus.HistogramVec
	bytesTotal                *prometheus.CounterVec
	streamsActive             *prometheus.GaugeVec
	streamsTotal              *prometheus.CounterVec
	streamErrorsTotal         *prometheus.CounterVec
	probeTotal                *prometheus.CounterVec
	probeDuration             *prometheus.HistogramVec
	registryLeaseTotal        *prometheus.CounterVec
	relayTotal                *prometheus.CounterVec
	agentConnections          *prometheus.GaugeVec
	agentConnectionCapacity   *prometheus.GaugeVec
	agentActiveStreams        *prometheus.GaugeVec
	agentConnectionRTT        *prometheus.HistogramVec
	agentConnectionErrors     *prometheus.CounterVec
	agentScaleDecisions       *prometheus.CounterVec
	agentSelection            *prometheus.CounterVec
	storageOperationDuration  *prometheus.HistogramVec
	storageErrorsTotal        *prometheus.CounterVec
	configReloadTotal         *prometheus.CounterVec
	ready                     *prometheus.GaugeVec
	streamStageDuration       *prometheus.HistogramVec
	streamOpenTotal           *prometheus.CounterVec
	streamQueueWait           *prometheus.HistogramVec
	streamWindowStall         *prometheus.HistogramVec
	streamBackpressure        *prometheus.CounterVec
	authorizationCache        *prometheus.CounterVec
	authorizationRevision     *prometheus.GaugeVec
	authorizationRevisionPoll *prometheus.CounterVec
	remoteValidationCache     *prometheus.CounterVec
	websshSessionsActive      *prometheus.GaugeVec
	websshTicketsCreatedTotal *prometheus.CounterVec
	websshTicketReuseTotal    *prometheus.CounterVec
	websshStreamErrorsTotal   *prometheus.CounterVec
	websshStreamDuration      *prometheus.HistogramVec
	websshBytesTotal          *prometheus.CounterVec
	activeMu                  sync.Mutex
	activeConnections         map[string]int
	activeStreams             map[string]int
	agentConnectionStates     map[string]bool
	agentConnectionStreams    map[string]int
}

// NewMetrics creates and registers a complete metrics set. Nil creates an isolated registry.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &Metrics{
		connectionsTotal:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_connections_total", Help: "Total connection attempts by result."}, []string{"component", "mode", "result", "error_class"}),
		connectionsActive:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_connections_active", Help: "Current active connections."}, []string{"component", "mode"}),
		connectionStageDuration:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_connection_stage_duration_seconds", Help: "Connection stage duration in seconds."}, []string{"component", "stage", "result", "error_class"}),
		heartbeatTotal:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_heartbeat_total", Help: "Total heartbeat outcomes."}, []string{"component", "result"}),
		heartbeatRTT:             prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_heartbeat_rtt_seconds", Help: "Heartbeat round-trip time in seconds."}, []string{"component"}),
		bytesTotal:               prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_bytes_total", Help: "Total bytes transferred."}, []string{"component", "direction", "protocol"}),
		streamsActive:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_streams_active", Help: "Current active logical streams."}, []string{"protocol"}),
		streamsTotal:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_streams_total", Help: "Total logical stream outcomes."}, []string{"protocol", "result", "error_class"}),
		streamErrorsTotal:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_stream_errors_total", Help: "Total logical stream errors."}, []string{"protocol", "error_class"}),
		probeTotal:               prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_probe_total", Help: "Total probe outcomes."}, []string{"probe_kind", "result", "error_class"}),
		probeDuration:            prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_probe_duration_seconds", Help: "Probe duration in seconds."}, []string{"probe_kind", "result", "error_class"}),
		registryLeaseTotal:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_registry_lease_total", Help: "Registry lease outcomes."}, []string{"operation", "result", "error_class"}),
		relayTotal:               prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_relay_total", Help: "Relay outcomes."}, []string{"result", "error_class"}),
		agentConnections:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_agent_connections", Help: "Current Agent connections, by identity."}, []string{"agent_id", "instance_id"}),
		agentConnectionCapacity:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_agent_connection_capacity", Help: "Maximum Agent connections allowed by policy."}, []string{"agent_id", "instance_id"}),
		agentActiveStreams:       prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_agent_active_streams", Help: "Active streams per Agent identity."}, []string{"agent_id", "instance_id"}),
		agentConnectionRTT:       prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_agent_connection_rtt_seconds", Help: "Agent connection heartbeat RTT in seconds."}, []string{"agent_id", "instance_id"}),
		agentConnectionErrors:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_agent_connection_errors_total", Help: "Agent connection errors."}, []string{"agent_id", "instance_id", "error_class"}),
		agentScaleDecisions:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_agent_connection_scale_decisions_total", Help: "Agent connection pool scale decisions."}, []string{"agent_id", "instance_id", "decision", "reason"}),
		agentSelection:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_agent_selection_total", Help: "Agent connection selection decisions."}, []string{"agent_id", "server_node_id", "scope"}),
		storageOperationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_storage_operation_duration_seconds", Help: "Storage operation duration in seconds."}, []string{"operation", "result"}),
		storageErrorsTotal:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_storage_errors_total", Help: "Storage errors."}, []string{"operation", "error_class"}),
		configReloadTotal:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_config_reload_total", Help: "Configuration reload outcomes."}, []string{"result", "error_class"}),
		ready:                    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tunnelmesh_ready", Help: "Readiness state, one when ready."}, []string{"component"}),
		streamStageDuration:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_stream_stage_duration_seconds", Help: "Stream lifecycle stage duration in seconds."}, []string{"component", "stage", "result", "error_class", "protocol"}),
		streamOpenTotal:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_stream_open_total", Help: "Stream open outcomes by negotiated mode."}, []string{"component", "result", "error_class", "open_mode"}),
		streamQueueWait:          prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_stream_queue_wait_seconds", Help: "Stream queue wait in seconds."}, []string{"component", "result"}),
		streamWindowStall:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tunnelmesh_stream_window_stall_seconds", Help: "Stream flow-control window stall duration in seconds."}, []string{"component", "result"}),
		streamBackpressure:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_stream_backpressure_total", Help: "Stream backpressure outcomes."}, []string{"component", "result"}),
		authorizationCache:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_authorization_cache_requests_total", Help: "Stream authorization cache requests."}, []string{"cache", "result"}),
		authorizationRevision: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "tunnelmesh_authorization_revision",
				Help: "Current authorization revision generation.",
			},
			nil,
		),
		authorizationRevisionPoll: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_authorization_revision_poll_total", Help: "Authorization revision poll outcomes."}, []string{"result", "error_class"}),
		remoteValidationCache:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tunnelmesh_remote_validation_cache_requests_total", Help: "Remote validation cache requests."}, []string{"cache", "result"}),
		websshSessionsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tunnelmesh_webssh_sessions_active", Help: "Current active browser WebSSH sessions.",
		}, nil),
		websshTicketsCreatedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tunnelmesh_webssh_tickets_created_total", Help: "Total WebSSH one-time tickets created.",
		}, nil),
		websshTicketReuseTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tunnelmesh_webssh_ticket_reuse_total", Help: "Total rejected WebSSH ticket authentication attempts.",
		}, []string{"reason"}),
		websshStreamErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tunnelmesh_webssh_streams_errors_total", Help: "Total WebSSH stream terminal errors.",
		}, []string{"error_class"}),
		websshStreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "tunnelmesh_webssh_stream_duration_seconds", Help: "WebSSH stream duration in seconds.",
		}, nil),
		websshBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tunnelmesh_webssh_bytes_total", Help: "Total bytes transferred by browser WebSSH sessions.",
		}, []string{"direction"}),
		activeConnections:      make(map[string]int),
		activeStreams:          make(map[string]int),
		agentConnectionStates:  make(map[string]bool),
		agentConnectionStreams: make(map[string]int),
	}
	reg.MustRegister(m.connectionsTotal, m.connectionsActive, m.connectionStageDuration, m.heartbeatTotal, m.heartbeatRTT, m.bytesTotal, m.streamsActive, m.streamsTotal, m.streamErrorsTotal, m.probeTotal, m.probeDuration, m.registryLeaseTotal, m.relayTotal, m.agentConnections, m.agentConnectionCapacity, m.agentActiveStreams, m.agentConnectionRTT, m.agentConnectionErrors, m.agentScaleDecisions, m.agentSelection, m.storageOperationDuration, m.storageErrorsTotal, m.configReloadTotal, m.ready, m.streamStageDuration, m.streamOpenTotal, m.streamQueueWait, m.streamWindowStall, m.streamBackpressure, m.authorizationCache, m.authorizationRevision, m.authorizationRevisionPoll, m.remoteValidationCache, m.websshSessionsActive, m.websshTicketsCreatedTotal, m.websshTicketReuseTotal, m.websshStreamErrorsTotal, m.websshStreamDuration, m.websshBytesTotal)
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

// NormalizeProtocol keeps peer-controlled protocol values within a finite
// Prometheus label set. Unknown extensions remain observable as "unknown".
func NormalizeProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp", "udp", "http", "websocket":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func (m *Metrics) ObserveConnection(component, mode, result, errorClass string) {
	component, mode, result, errorClass = label(component), label(mode), label(result), label(errorClass)
	m.connectionsTotal.WithLabelValues(component, mode, result, errorClass).Inc()
	key := component + "\x00" + mode
	m.activeMu.Lock()
	switch result {
	case "started", "active", "connected":
		m.activeConnections[key]++
	case "closed", "disconnected", "failed", "error":
		if m.activeConnections[key] > 0 {
			m.activeConnections[key]--
		}
	}
	value := m.activeConnections[key]
	m.connectionsActive.WithLabelValues(component, mode).Set(float64(value))
	m.activeMu.Unlock()
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
		m.bytesTotal.WithLabelValues(label(component), label(direction), NormalizeProtocol(protocol)).Add(float64(n))
	}
}

func (m *Metrics) ObserveStream(protocol, result, errorClass string) {
	protocol, result, errorClass = NormalizeProtocol(protocol), label(result), label(errorClass)
	m.streamsTotal.WithLabelValues(protocol, result, errorClass).Inc()
	if errorClass != "" {
		m.streamErrorsTotal.WithLabelValues(protocol, errorClass).Inc()
	}
	key := protocol
	m.activeMu.Lock()
	switch result {
	case "accepted", "started", "active", "open":
		m.activeStreams[key]++
	case "closed", "reset", "rejected", "failed":
		if m.activeStreams[key] > 0 {
			m.activeStreams[key]--
		}
	}
	value := m.activeStreams[key]
	m.streamsActive.WithLabelValues(protocol).Set(float64(value))
	m.activeMu.Unlock()
}

func (m *Metrics) ObserveProbe(kind, result, errorClass string, duration time.Duration) {
	kind, result, errorClass = label(kind), label(result), label(errorClass)
	m.probeTotal.WithLabelValues(kind, result, errorClass).Inc()
	if duration >= 0 {
		m.probeDuration.WithLabelValues(kind, result, errorClass).Observe(duration.Seconds())
	}
}

func (m *Metrics) ObserveRegistryLease(operation, result, errorClass string) {
	m.registryLeaseTotal.WithLabelValues(label(operation), label(result), label(errorClass)).Inc()
}

func (m *Metrics) ObserveRelay(result, errorClass string) {
	m.relayTotal.WithLabelValues(label(result), label(errorClass)).Inc()
}

func (m *Metrics) ObserveAgentConnection(agentID, instanceID, connectionID string, active bool) {
	agentID, instanceID = label(agentID), label(instanceID)
	connectionKey := agentID + "\x00" + instanceID + "\x00" + label(connectionID)
	identityKey := agentID + "\x00" + instanceID
	m.activeMu.Lock()
	if active {
		m.agentConnectionStates[connectionKey] = true
	} else if !m.agentConnectionStates[connectionKey] {
		m.activeMu.Unlock()
		return
	} else {
		delete(m.agentConnectionStates, connectionKey)
		delete(m.agentConnectionStreams, connectionKey)
	}
	count := 0
	for key, isActive := range m.agentConnectionStates {
		if isActive && strings.HasPrefix(key, identityKey+"\x00") {
			count++
		}
	}
	m.agentConnections.WithLabelValues(agentID, instanceID).Set(float64(count))
	m.activeMu.Unlock()
}

func (m *Metrics) ObserveAgentConnectionError(agentID, instanceID, connectionID, errorClass string) {
	_ = connectionID
	m.agentConnectionErrors.WithLabelValues(label(agentID), label(instanceID), label(errorClass)).Inc()
}

func (m *Metrics) ObserveAgentActiveStreams(agentID, instanceID, connectionID string, active int) {
	if active < 0 {
		active = 0
	}
	agentID, instanceID = label(agentID), label(instanceID)
	connectionKey := agentID + "\x00" + instanceID + "\x00" + label(connectionID)
	m.activeMu.Lock()
	m.agentConnectionStreams[connectionKey] = active
	total := 0
	for key, count := range m.agentConnectionStreams {
		if strings.HasPrefix(key, agentID+"\x00"+instanceID+"\x00") {
			total += count
		}
	}
	m.agentActiveStreams.WithLabelValues(agentID, instanceID).Set(float64(total))
	m.activeMu.Unlock()
}

func (m *Metrics) ObserveAgentConnectionRTT(agentID, instanceID, connectionID string, rtt time.Duration) {
	if rtt < 0 {
		rtt = 0
	}
	_ = connectionID
	m.agentConnectionRTT.WithLabelValues(label(agentID), label(instanceID)).Observe(rtt.Seconds())
}

func (m *Metrics) SetAgentConnectionCapacity(agentID, instanceID string, capacity int) {
	if capacity < 0 {
		capacity = 0
	}
	m.agentConnectionCapacity.WithLabelValues(label(agentID), label(instanceID)).Set(float64(capacity))
}

func (m *Metrics) ObserveAgentScaleDecision(agentID, instanceID, decision, reason string) {
	m.agentScaleDecisions.WithLabelValues(label(agentID), label(instanceID), label(decision), label(reason)).Inc()
}

func (m *Metrics) ObserveAgentSelection(agentID, connectionID, serverNodeID, scope string) {
	_ = connectionID
	m.agentSelection.WithLabelValues(label(agentID), label(serverNodeID), label(scope)).Inc()
}

func (m *Metrics) ObserveStorage(operation, result, errorClass string, duration time.Duration) {
	operation, result, errorClass = label(operation), label(result), label(errorClass)
	if duration < 0 {
		duration = 0
	}
	m.storageOperationDuration.WithLabelValues(operation, result).Observe(duration.Seconds())
	if errorClass != "" {
		m.storageErrorsTotal.WithLabelValues(operation, errorClass).Inc()
	}
}

func (m *Metrics) ObserveConfigReload(result, errorClass string) {
	m.configReloadTotal.WithLabelValues(label(result), label(errorClass)).Inc()
}

func (m *Metrics) ObserveStreamStage(component, stage, result, errorClass, protocol string, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.streamStageDuration.WithLabelValues(label(component), label(stage), label(result), label(errorClass), NormalizeProtocol(protocol)).Observe(duration.Seconds())
}

func (m *Metrics) ObserveStreamOpen(component, result, errorClass, openMode string) {
	m.streamOpenTotal.WithLabelValues(label(component), label(result), label(errorClass), label(openMode)).Inc()
}

func (m *Metrics) ObserveStreamQueueWait(component, result string, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.streamQueueWait.WithLabelValues(label(component), label(result)).Observe(duration.Seconds())
}

func (m *Metrics) ObserveStreamWindowStall(component, result string, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.streamWindowStall.WithLabelValues(label(component), label(result)).Observe(duration.Seconds())
}

func (m *Metrics) ObserveStreamBackpressure(component, result string) {
	m.streamBackpressure.WithLabelValues(label(component), label(result)).Inc()
}

func (m *Metrics) ObserveAuthorizationCache(cache, result string) {
	m.authorizationCache.WithLabelValues(label(cache), label(result)).Inc()
}

func (m *Metrics) SetAuthorizationRevision(revision uint64) {
	m.authorizationRevision.WithLabelValues().Set(float64(revision))
}

func (m *Metrics) ObserveAuthorizationRevisionPoll(result, errorClass string) {
	m.authorizationRevisionPoll.WithLabelValues(label(result), label(errorClass)).Inc()
}

func (m *Metrics) ObserveRemoteValidationCache(cache, result string) {
	m.remoteValidationCache.WithLabelValues(label(cache), label(result)).Inc()
}

func (m *Metrics) SetReady(component string, ready bool) {
	value := float64(0)
	if ready {
		value = 1
	}
	m.ready.WithLabelValues(label(component)).Set(value)
}

func (m *Metrics) SetWebSSHActiveSessions(active int) {
	if active < 0 {
		active = 0
	}
	m.websshSessionsActive.WithLabelValues().Set(float64(active))
}

func (m *Metrics) ObserveWebSSHTicketCreated() {
	m.websshTicketsCreatedTotal.WithLabelValues().Inc()
}

func (m *Metrics) ObserveWebSSHTicketReuse(reason string) {
	m.websshTicketReuseTotal.WithLabelValues(label(reason)).Inc()
}

func (m *Metrics) ObserveWebSSHStreamError(errorClass string) {
	m.websshStreamErrorsTotal.WithLabelValues(label(errorClass)).Inc()
}

func (m *Metrics) ObserveWebSSHStreamDuration(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.websshStreamDuration.WithLabelValues().Observe(duration.Seconds())
}

func (m *Metrics) ObserveWebSSHBytes(direction string, n int64) {
	if n > 0 {
		m.websshBytesTotal.WithLabelValues(label(direction)).Add(float64(n))
	}
}
