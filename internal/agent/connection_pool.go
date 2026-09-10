package agent

import (
	"context"
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
}

func (h *connectionPoolHandler) Handle(frame protocol.Frame) error {
	if frame.Type == protocol.FrameAgentMetadataAck {
		if ack, err := protocol.DecodeAgentMetadataAckPayload(frame.Payload); err == nil {
			h.supported = ack.ConnectionPoolSupported
		}
	}
	h.report(0)
	if h.inner == nil {
		return nil
	}
	return h.inner.Handle(frame)
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
	h.controller.Report(h.connectionID, ConnectionStats{
		ConnectionPoolSupported: h.supported, ActiveStreams: active, RTT: rtt,
	})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
