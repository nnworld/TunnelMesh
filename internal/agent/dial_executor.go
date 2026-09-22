package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var ErrDialQueueFull = errors.New("agent: dial queue full")
var ErrDialExecutorClosed = errors.New("agent: dial executor closed")

// DialExecutorConfig bounds concurrent target dials and queued opens. The
// limits prevent one slow target from consuming the entire Agent process.
type DialExecutorConfig struct {
	MaxConcurrent  int
	MaxPending     int
	ConnectTimeout time.Duration
	OpenTimeout    time.Duration
}

type DialRequest struct {
	Frame   protocol.Frame
	Payload protocol.StreamOpenPayload
	Result  func(DialResult)
}

type AgentStreamConfig = config.AgentStreamConfig

type DialResult struct {
	Conn       io.ReadWriteCloser
	Stage      protocol.OpenResultStage
	Code       protocol.OpenResultCode
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

type dialJob struct {
	ctx     context.Context
	request DialRequest
	cancel  context.CancelFunc
}

type DialExecutor struct {
	ctx         context.Context
	cancel      context.CancelFunc
	config      DialExecutorConfig
	dial        streamPayloadDialFunc
	queue       chan dialJob
	mu          sync.Mutex
	cancels     map[uint32]context.CancelFunc
	closeOnce   sync.Once
	workers     sync.WaitGroup
	openSamples latencySamples
}

func NewDialExecutor(config DialExecutorConfig) *DialExecutor {
	return NewDialExecutorWithDial(config, nil)
}

func NewDialExecutorWithDial(config DialExecutorConfig, dial streamPayloadDialFunc) *DialExecutor {
	config.normalize()
	if dial == nil {
		dial = Dialer{Timeout: config.ConnectTimeout}.dialStreamPayload
	}
	ctx, cancel := context.WithCancel(context.Background())
	executor := &DialExecutor{
		ctx: ctx, cancel: cancel, config: config, dial: dial,
		queue: make(chan dialJob, config.MaxPending), cancels: make(map[uint32]context.CancelFunc),
	}
	executor.workers.Add(config.MaxConcurrent)
	for i := 0; i < config.MaxConcurrent; i++ {
		go executor.worker()
	}
	return executor
}

func (c *DialExecutorConfig) normalize() {
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 1
	}
	if c.MaxPending <= 0 {
		c.MaxPending = 1
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 5 * time.Second
	}
	if c.OpenTimeout <= 0 {
		c.OpenTimeout = 8 * time.Second
	}
}

// AgentStreamCapabilities reports what this agent process can actually serve.
//
// icmpReady is a separate fact from streams.ICMPEnabled because the
// configuration is a wish and the engine is the outcome: a host whose
// net.ipv4.ping_group_range excludes the process gid cannot open the socket no
// matter what the file says. Advertising a capability the agent cannot honour
// would make the server sign ICMP peers and send echoes that answer
// "unsupported stream protocol", which is a failure the operator cannot see
// from either side of the negotiation.
func AgentStreamCapabilities(streams AgentStreamConfig, icmpReady bool) []string {
	capabilities := []string{protocol.CapabilityStreamOpenResult}
	if streams.ICMPEnabled && icmpReady {
		capabilities = append(capabilities, protocol.CapabilityStreamICMPEcho)
	}
	return capabilities
}

func (e *DialExecutor) Submit(ctx context.Context, request DialRequest) error {
	if e == nil {
		return ErrDialExecutorClosed
	}
	if request.Frame.StreamID == 0 || request.Result == nil {
		return errors.New("agent: invalid dial request")
	}
	select {
	case <-e.ctx.Done():
		return ErrDialExecutorClosed
	default:
	}
	streamCtx, cancel := context.WithCancel(ctx)
	job := dialJob{ctx: streamCtx, request: request, cancel: cancel}
	e.mu.Lock()
	if _, exists := e.cancels[request.Frame.StreamID]; exists {
		e.mu.Unlock()
		cancel()
		return errors.New("agent: duplicate dial stream")
	}
	e.cancels[request.Frame.StreamID] = cancel
	e.mu.Unlock()

	select {
	case e.queue <- job:
		return nil
	case <-e.ctx.Done():
		e.removeCancel(request.Frame.StreamID)
		cancel()
		return ErrDialExecutorClosed
	default:
		e.removeCancel(request.Frame.StreamID)
		cancel()
		return ErrDialQueueFull
	}
}

func (e *DialExecutor) Cancel(streamID uint32) {
	if e == nil {
		return
	}
	e.mu.Lock()
	cancel := e.cancels[streamID]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *DialExecutor) PendingDials() int {
	if e == nil {
		return 0
	}
	return len(e.queue)
}

func (e *DialExecutor) OpenP95() time.Duration {
	if e == nil {
		return 0
	}
	return e.openSamples.P95()
}

func (e *DialExecutor) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		e.cancel()
		close(e.queue)
	})
	e.workers.Wait()
	return nil
}

func (e *DialExecutor) worker() {
	defer e.workers.Done()
	for job := range e.queue {
		started := time.Now()
		timeout := e.config.OpenTimeout
		if e.config.ConnectTimeout > 0 && e.config.ConnectTimeout < timeout {
			timeout = e.config.ConnectTimeout
		}
		ctx, cancel := context.WithTimeout(job.ctx, timeout)
		conn, dialErr := e.dial(ctx, job.request.Payload)
		cancel()
		result := newDialResult(conn, dialErr)
		e.openSamples.Record(time.Since(started))
		job.request.Result(result)
		e.removeCancel(job.request.Frame.StreamID)
		job.cancel()
	}
}

func (e *DialExecutor) removeCancel(streamID uint32) {
	e.mu.Lock()
	delete(e.cancels, streamID)
	e.mu.Unlock()
}

func newDialResult(conn io.ReadWriteCloser, err error) DialResult {
	if err == nil {
		return DialResult{Conn: conn, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}
	}
	result := DialResult{Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeInternalError, Err: fmt.Errorf("agent: target connect failed")}
	if errors.Is(err, context.Canceled) {
		result.Code = protocol.OpenResultCodeTimeout
		return result
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		result.Code = protocol.OpenResultCodeTimeout
		result.Retryable = true
		return result
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		result.Stage = protocol.OpenResultStageDNS
		result.Code = protocol.OpenResultCodeHostUnreachable
		return result
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		result.Code = protocol.OpenResultCodeConnectionRefused
	case strings.Contains(message, "network is unreachable"):
		result.Code = protocol.OpenResultCodeNetworkUnreachable
		result.Retryable = true
	case strings.Contains(message, "no route to host"):
		result.Code = protocol.OpenResultCodeHostUnreachable
		result.Retryable = true
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded"):
		result.Code = protocol.OpenResultCodeTimeout
		result.Retryable = true
	}
	return result
}
