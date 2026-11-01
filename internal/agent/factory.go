package agent

import (
	"log/slog"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// NewStreamDispatcherFactory builds the standard Agent stream handler. It is the
// single implementation shared by the standalone Agent and the embedded Agent role
// of a Client, so both paths negotiate the same capabilities.
//
// echoer is optional: readiness is the engine, not the configuration, so a nil
// echoer keeps ICMP out of the advertisement instead of promising a stream the
// host cannot serve.
func NewStreamDispatcherFactory(streams config.AgentStreamConfig, echoer *Echoer) ConnectionSessionFactory {
	return NewStreamDispatcherFactoryWithExec(streams, echoer, ExecConfig{})
}

// NewStreamDispatcherFactoryWithExec is the shared factory with script execution
// configured. A zero ExecConfig leaves execution off and the capability unadvertised.
func NewStreamDispatcherFactoryWithExec(streams config.AgentStreamConfig, echoer *Echoer, execConfig ExecConfig) ConnectionSessionFactory {
	execRunner := newExecRunner(execConfig)
	return func(session *Session, _ string) SessionFrameHandler {
		capabilities := AgentStreamCapabilities(streams, echoer != nil)
		if execRunner.Config().Enabled {
			capabilities = append(capabilities, protocol.CapabilityAgentExec)
		}
		session.SetCapabilities(capabilities)
		dispatcher := NewStreamDispatcherWithConfig(Dialer{ICMPEcho: echoer, Exec: execRunner}, nil, session.Send, DialExecutorConfig{
			MaxConcurrent:  streams.MaxConcurrentDials,
			MaxPending:     streams.MaxPendingDials,
			ConnectTimeout: streams.ConnectTimeout,
			OpenTimeout:    streams.OpenTimeout,
		}, nil)
		dispatcher.SetMaxActiveStreams(streams.MaxActive)
		// A rejected buffer size leaves the dispatcher on its default; serving with the
		// default is safer than refusing every connection, but the operator must see it.
		if err := dispatcher.SetInboundBufferBytes(streams.InboundBufferBytes); err != nil {
			slog.Warn("agent inbound buffer rejected", "configured_bytes", streams.InboundBufferBytes, "error", err)
		}
		return dispatcher
	}
}
