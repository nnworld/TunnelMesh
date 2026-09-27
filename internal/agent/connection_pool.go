package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// WebSocketPoolOptions contains the single Server URL and identity shared by
// every physical connection. Multiple server_urls are intentionally absent.
type WebSocketPoolOptions struct {
	ServerURL          string
	Token              string
	AgentID            string
	NodeID             string
	InstanceID         string
	Epoch              int64
	Collector          *MetadataCollector
	Factory            ConnectionSessionFactory
	WebSocket          WebSocketRunOptions
	Min                int
	Max                int
	HighWatermark      int
	LowWatermark       int
	EvaluationInterval time.Duration
	Cooldown           time.Duration
	HighRTT            time.Duration
}

// RunConnectionPool runs adaptive physical connections to one Server URL.
func RunConnectionPool(ctx context.Context, options WebSocketPoolOptions) error {
	var controller *ConnectionController
	controller = NewConnectionController(ConnectionControllerOptions{
		AgentID: options.AgentID, InstanceID: options.InstanceID, Metrics: options.WebSocket.Metrics,
		Min: options.Min, Max: options.Max, HighWatermark: options.HighWatermark,
		LowWatermark: options.LowWatermark, EvaluationInterval: options.EvaluationInterval,
		Cooldown: options.Cooldown, HighRTT: options.HighRTT,
		Runner: func(connectionCtx context.Context, connectionID string, connectionEpoch int64) error {
			return runWebSocketConnection(
				connectionCtx, options.ServerURL, options.Token, options.AgentID, options.NodeID,
				options.InstanceID, connectionID, max64(options.Epoch, connectionEpoch), options.Collector,
				nil, func(session *Session) SessionFrameHandler {
					var inner SessionFrameHandler
					if options.Factory != nil {
						inner = options.Factory(session, connectionID)
					}
					handler := &connectionPoolHandler{
						controller: controller, connectionID: connectionID,
						session: session, inner: inner,
						agentID: options.AgentID, configuredMax: options.Max,
					}
					if dispatcher, ok := inner.(*StreamDispatcher); ok {
						handler.dispatcher = dispatcher
					}
					session.OnHeartbeatRTT = handler.observeRTT
					return handler
				}, options.WebSocket,
			)
		},
	})
	return controller.Run(ctx)
}

type connectionPoolHandler struct {
	controller   *ConnectionController
	connectionID string
	session      *Session
	dispatcher   *StreamDispatcher
	inner        SessionFrameHandler
	supported    bool
	// agentID and configuredMax exist only to interpret the Server's ack: the pool
	// size is a local wish, the acked ceiling is the remote fact, and the gap between
	// them is otherwise visible only as a connection that keeps getting refused.
	agentID       string
	configuredMax int
	// ceilingWarned dedupes per distinct ceiling, because metadata acks repeat on
	// every report and this is a configuration finding, not a runtime event.
	ceilingWarned int
}

func (h *connectionPoolHandler) Handle(frame protocol.Frame) error {
	if frame.Type == protocol.FrameAgentMetadataAck {
		if ack, err := protocol.DecodeAgentMetadataAckPayload(frame.Payload); err == nil {
			h.supported = ack.ConnectionPoolSupported
			h.checkServerCeiling(ack.MaxConnectionsPerAgent)
			if h.dispatcher != nil {
				strictOpen := false
				icmpEcho := false
				for _, capability := range ack.Capabilities {
					switch capability {
					case protocol.CapabilityStreamOpenResult:
						strictOpen = true
					case protocol.CapabilityStreamICMPEcho:
						icmpEcho = true
					}
				}
				h.dispatcher.SetOpenResultEnabled(strictOpen)
				h.dispatcher.SetICMPEchoEnabled(icmpEcho)
			}
		}
	}
	h.report(0)
	if h.inner == nil {
		return nil
	}
	return h.inner.Handle(frame)
}

// checkServerCeiling warns once per distinct ceiling when the Server enforces fewer
// connections than the pool is configured to open. The two settings live on different
// hosts and are deliberately not cross-checked at startup, so this runtime ack is the
// only place where the mismatch can be named. A zero ceiling means an older Server that
// omits the field, which is not the same as "no connections allowed" and stays silent.
func (h *connectionPoolHandler) checkServerCeiling(serverMax int) {
	if serverMax <= 0 || h.configuredMax <= serverMax || h.ceilingWarned == serverMax {
		return
	}
	h.ceilingWarned = serverMax
	slog.Warn("agent connection pool exceeds the server ceiling",
		"agent_id", h.agentID, "connection_id", h.connectionID,
		"configured_max", h.configuredMax, "server_max", serverMax,
		"hint", "raise server.agents.max_connections_per_agent or lower agent.connections.max")
}

func (h *connectionPoolHandler) Close() error {
	if h.inner == nil {
		return nil
	}
	return h.inner.Close()
}

func (h *connectionPoolHandler) observeRTT(rtt time.Duration) {
	h.report(rtt)
}

func (h *connectionPoolHandler) report(rtt time.Duration) {
	if h.controller == nil {
		return
	}
	var active int64
	if h.dispatcher != nil {
		active = int64(h.dispatcher.ActiveStreams())
	}
	if rtt == 0 {
		rtt = h.session.LastHeartbeatRTT()
	}
	stats := ConnectionStats{
		ConnectionPoolSupported: h.supported, ActiveStreams: active, RTT: rtt,
	}
	if source, ok := h.inner.(interface {
		PendingDials() int
		OpenP95() time.Duration
		TTFBP95() time.Duration
	}); ok {
		stats.PendingDials = source.PendingDials()
		stats.OpenP95 = source.OpenP95()
		stats.TTFBP95 = source.TTFBP95()
	}
	if source, ok := h.inner.(interface{ WriterQueueWaitP95() time.Duration }); ok {
		stats.WriterQueueWait = source.WriterQueueWaitP95()
	}
	if h.session != nil {
		if wait := h.session.WriterQueueWaitP95(); wait > stats.WriterQueueWait {
			stats.WriterQueueWait = wait
		}
	}
	h.controller.Report(h.connectionID, stats)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
