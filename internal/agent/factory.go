package agent

import (
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// NewStreamDispatcherFactory builds the standard Agent stream handler without
// enabling optional ICMP support. It is shared by the standalone Agent and the
// embedded Agent role of a Client so both paths negotiate the same capabilities.
func NewStreamDispatcherFactory(streams config.AgentStreamConfig) ConnectionSessionFactory {
	return func(session *Session, _ string) SessionFrameHandler {
		session.SetCapabilities(AgentStreamCapabilities(streams, false))
		dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, session.Send, DialExecutorConfig{
			MaxConcurrent:  streams.MaxConcurrentDials,
			MaxPending:     streams.MaxPendingDials,
			ConnectTimeout: streams.ConnectTimeout,
			OpenTimeout:    streams.OpenTimeout,
		}, nil)
		dispatcher.SetMaxActiveStreams(streams.MaxActive)
		if err := dispatcher.SetInboundBufferBytes(streams.InboundBufferBytes); err != nil {
			return dispatcher
		}
		return dispatcher
	}
}
