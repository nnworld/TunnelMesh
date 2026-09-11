package server

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

var ErrClientOpenQueueFull = errors.New("server: client open queue full")

type ClientStreamServiceConfig struct {
	MaxConcurrentOpens int
	MaxPendingOpens    int
	OpenTimeout        time.Duration
	Observability      *ClientObservabilityService
}

type OpenFuture interface {
	Wait(ctx context.Context) (protocol.OpenResultPayload, error)
	Cancel()
	Stream() io.ReadWriteCloser
}

type clientOpenResult struct {
	payload protocol.OpenResultPayload
	conn    io.ReadWriteCloser
}

type clientOpenFuture struct {
	result <-chan clientOpenResult
	cancel context.CancelFunc
	once   sync.Once
	value  clientOpenResult
}

func (f *clientOpenFuture) Wait(ctx context.Context) (protocol.OpenResultPayload, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case value := <-f.result:
		f.value = value
		return value.payload, nil
	case <-ctx.Done():
		return protocol.OpenResultPayload{}, ctx.Err()
	}
}

func (f *clientOpenFuture) Cancel() {
	f.once.Do(f.cancel)
}

func (f *clientOpenFuture) Stream() io.ReadWriteCloser {
	return f.value.conn
}

type clientOpenJob struct {
	principal ClientSessionPrincipal
	streamID  uint32
	request   protocol.StreamOpenPayload
	ctx       context.Context
	cancel    context.CancelFunc
	result    chan clientOpenResult
}

type ClientStreamService struct {
	authorizer    StreamAuthorizer
	transport     relay.NodeTransport
	config        ClientStreamServiceConfig
	observability *ClientObservabilityService
	metrics       *observability.Metrics
	queue         chan clientOpenJob
	closeOnce     sync.Once
	workers       sync.WaitGroup
}

func NewClientStreamService(authorizer StreamAuthorizer, transport relay.NodeTransport, config ClientStreamServiceConfig, metrics *observability.Metrics) *ClientStreamService {
	if config.MaxConcurrentOpens <= 0 {
		config.MaxConcurrentOpens = 1
	}
	if config.MaxPendingOpens <= 0 {
		config.MaxPendingOpens = 1
	}
	if config.OpenTimeout <= 0 {
		config.OpenTimeout = 8 * time.Second
	}
	service := &ClientStreamService{
		authorizer: authorizer, transport: transport, config: config,
		observability: config.Observability, metrics: metrics,
		queue: make(chan clientOpenJob, config.MaxPendingOpens),
	}
	service.workers.Add(config.MaxConcurrentOpens)
	for i := 0; i < config.MaxConcurrentOpens; i++ {
		go service.worker()
	}
	return service
}

func (s *ClientStreamService) Open(ctx context.Context, principal ClientSessionPrincipal, streamID uint32, request protocol.StreamOpenPayload) OpenFuture {
	result := make(chan clientOpenResult, 1)
	if s == nil || streamID == 0 {
		result <- clientOpenResult{payload: failureResult(protocol.OpenResultStageProtocol, protocol.OpenResultCodeInternalError)}
		return &clientOpenFuture{result: result, cancel: func() {}}
	}
	jobCtx, cancel := context.WithCancel(ctx)
	job := clientOpenJob{principal: principal, streamID: streamID, request: request, ctx: jobCtx, cancel: cancel, result: result}
	select {
	case s.queue <- job:
		return &clientOpenFuture{result: result, cancel: cancel}
	default:
		cancel()
		result <- clientOpenResult{payload: failureResult(protocol.OpenResultStageQueue, protocol.OpenResultCodeQueueFull)}
		return &clientOpenFuture{result: result, cancel: func() {}}
	}
}

func (s *ClientStreamService) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		for {
			select {
			case job := <-s.queue:
				job.cancel()
				job.result <- clientOpenResult{payload: failureResult(protocol.OpenResultStageQueue, protocol.OpenResultCodeInternalError)}
			default:
				close(s.queue)
				return
			}
		}
	})
	s.workers.Wait()
	return nil
}

func (s *ClientStreamService) worker() {
	defer s.workers.Done()
	for job := range s.queue {
		// The relay transport may bind the returned stream to this context
		// (gRPC does). Cancel only while opening; a successful stream must
		// remain owned by the caller/session until it is closed.
		streamCtx, cancel := context.WithCancel(job.ctx)
		openDone := make(chan struct{})
		timeoutDone := make(chan struct{})
		timedOut := make(chan struct{})
		timer := time.NewTimer(s.config.OpenTimeout)
		go func() {
			defer close(timeoutDone)
			select {
			case <-timer.C:
				close(timedOut)
				cancel()
			case <-openDone:
				timer.Stop()
			}
		}()
		result := s.open(streamCtx, job)
		close(openDone)
		<-timeoutDone
		if result.conn != nil && streamCtx.Err() != nil {
			_ = result.conn.Close()
			result = clientOpenResult{payload: failureResult(protocol.OpenResultStageQueue, protocol.OpenResultCodeTimeout)}
		}
		if result.conn == nil {
			select {
			case <-timedOut:
				result = clientOpenResult{payload: failureResult(protocol.OpenResultStageQueue, protocol.OpenResultCodeTimeout)}
			default:
			}
		}
		job.result <- result
		if result.conn == nil {
			cancel()
			job.cancel()
		}
	}
}

func (s *ClientStreamService) open(ctx context.Context, job clientOpenJob) clientOpenResult {
	started := time.Now()
	if s.authorizer == nil || s.transport == nil {
		return clientOpenResult{payload: failureResult(protocol.OpenResultStageAuthorization, protocol.OpenResultCodeInternalError)}
	}
	if err := s.authorizer.Authorize(ctx, job.principal, job.request); err != nil {
		code := protocol.OpenResultCodeForbidden
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = protocol.OpenResultCodeTimeout
		}
		if s.metrics != nil {
			s.metrics.ObserveStreamStage("server", "authorization", "failure", observability.NormalizeErrorClass(err), job.request.Protocol, time.Since(started))
		}
		return clientOpenResult{payload: failureResult(protocol.OpenResultStageAuthorization, code)}
	}
	if s.metrics != nil {
		s.metrics.ObserveStreamStage("server", "authorization", "success", "", job.request.Protocol, time.Since(started))
	}
	request := relay.StreamRequest{
		StreamID: job.streamID, AgentID: job.request.AgentID, Protocol: job.request.Protocol,
		StrictOpen: job.principal.StrictOpen,
		TargetHost: job.request.TargetHost, TargetPort: job.request.TargetPort,
		Metadata: append([]byte(nil), job.request.Metadata...),
	}
	var conn io.ReadWriteCloser
	var err error
	var openResult protocol.OpenResultPayload
	if job.principal.StrictOpen {
		resultTransport, ok := s.transport.(relay.OpenResultTransport)
		if !ok {
			return clientOpenResult{payload: failureResult(protocol.OpenResultStageRelay, protocol.OpenResultCodeUnsupportedCapability)}
		}
		var relayResult relay.RelayOpenResult
		conn, relayResult, err = resultTransport.OpenStreamResult(ctx, request)
		openResult = relayResult.Payload
	} else {
		conn, err = s.transport.OpenStream(ctx, request)
		openResult = protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}
	}
	if err != nil || conn == nil {
		code := protocol.OpenResultCodeInternalError
		if errors.Is(err, relay.ErrNodeDisconnected) {
			code = protocol.OpenResultCodeAgentOffline
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = protocol.OpenResultCodeTimeout
		}
		return clientOpenResult{payload: failureResult(protocol.OpenResultStageRelay, code)}
	}
	payload := openResult
	if !payload.Accepted {
		return clientOpenResult{payload: payload}
	}
	if s.metrics != nil {
		s.metrics.ObserveStreamOpen("server", "success", "", "strict")
	}
	return clientOpenResult{payload: payload, conn: conn}
}

func failureResult(stage protocol.OpenResultStage, code protocol.OpenResultCode) protocol.OpenResultPayload {
	return protocol.OpenResultPayload{Accepted: false, Stage: stage, Code: code, Retryable: code == protocol.OpenResultCodeQueueFull || code == protocol.OpenResultCodeTimeout}
}
