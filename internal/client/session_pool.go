package client

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var ErrAgentSessionUnavailable = errors.New("client: agent session unavailable")

// SessionPoolConfig controls one Agent's physical WebSocket connection pool.
type SessionPoolConfig struct {
	Min                int
	Max                int
	HighWatermark      int
	LowWatermark       int
	EvaluationInterval time.Duration
	Cooldown           time.Duration
}

// SessionPoolRunner starts one physical WebSocket. The pool supplies final
// run options, including the connection slot and metadata snapshot, so custom
// runners cannot accidentally omit observability identity.
type SessionPoolRunner func(ctx context.Context, serverURL, token string, onReady func(*Session) error, options WebSocketRunOptions) error

type SessionPoolManagerOptions struct {
	ServerURL string
	Token     string
	Config    SessionPoolConfig
	Runner    SessionPoolRunner
	WebSocket WebSocketRunOptions
	Metadata  ClientMetadataOptions
}

type SessionPoolManager struct {
	options SessionPoolManagerOptions

	mu    sync.RWMutex
	pools map[string]*agentSessionPool
}

func NewSessionPoolManager(options SessionPoolManagerOptions) *SessionPoolManager {
	return &SessionPoolManager{options: options, pools: make(map[string]*agentSessionPool)}
}

// Opener returns a stream opener bound to one logical Agent. Calling it before
// Run also registers the Agent as a pool that must be started.
func (m *SessionPoolManager) Opener(agentID string) StreamOpener {
	agentID = strings.TrimSpace(agentID)
	m.mu.Lock()
	defer m.mu.Unlock()
	pool, exists := m.pools[agentID]
	if !exists {
		pool = newAgentSessionPool(agentID, m.options)
		m.pools[agentID] = pool
	}
	return PooledStreamOpener{pool: pool}
}

func (m *SessionPoolManager) Run(ctx context.Context) error {
	m.mu.RLock()
	pools := make([]*agentSessionPool, 0, len(m.pools))
	for _, pool := range m.pools {
		pools = append(pools, pool)
	}
	m.mu.RUnlock()
	if len(pools) == 0 {
		return errors.New("client: session pool has no agents")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, len(pools))
	for _, pool := range pools {
		pool := pool
		go func() { results <- pool.run(runCtx) }()
	}

	var runErr error
	for i := 0; i < len(pools); i++ {
		select {
		case err := <-results:
			if err != nil && runErr == nil {
				runErr = err
				cancel()
			}
		case <-runCtx.Done():
			if runErr == nil {
				select {
				case err := <-results:
					if err != nil {
						runErr = err
					}
				default:
				}
			}
			if runErr == nil {
				runErr = ctx.Err()
			}
			cancel()
		}
	}
	return runErr
}

func (m *SessionPoolManager) OpenCount(agentID string) int {
	pool := m.pool(agentID)
	if pool == nil {
		return 0
	}
	return pool.slotCount()
}

func (m *SessionPoolManager) ActiveStreams(agentID string) []int64 {
	pool := m.pool(agentID)
	if pool == nil {
		return nil
	}
	return pool.activeStreams()
}

func (m *SessionPoolManager) pool(agentID string) *agentSessionPool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pools[strings.TrimSpace(agentID)]
}

type sessionPoolSlot struct {
	id     int
	cancel context.CancelFunc
}

type pooledSession struct {
	slotID  int
	session *Session
	active  int64
	lastRTT atomic.Int64
}

type agentSessionPool struct {
	agentID string
	options SessionPoolManagerOptions

	mu        sync.RWMutex
	slots     map[int]*sessionPoolSlot
	sessions  map[int]*pooledSession
	nextSlot  int
	lastScale time.Time
	errCh     chan error
	runCtx    context.Context
}

func newAgentSessionPool(agentID string, options SessionPoolManagerOptions) *agentSessionPool {
	return &agentSessionPool{
		agentID: agentID, options: options,
		slots: make(map[int]*sessionPoolSlot), sessions: make(map[int]*pooledSession),
	}
}

func (p *agentSessionPool) run(ctx context.Context) error {
	// The error channel is buffered and the pool context is stored so a runner
	// goroutine can publish a terminal failure even after run() has returned.
	// Otherwise a late runner error would leak the goroutine on channel send.
	p.errCh = make(chan error, 1)
	p.runCtx = ctx
	for i := 0; i < p.options.Config.Min; i++ {
		p.startSlot(ctx)
	}

	interval := p.options.Config.EvaluationInterval
	if interval <= 0 {
		select {
		case <-ctx.Done():
			p.cancelAll()
			return ctx.Err()
		case err := <-p.errCh:
			p.cancelAll()
			return err
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.cancelAll()
			return ctx.Err()
		case err := <-p.errCh:
			p.cancelAll()
			return err
		case <-ticker.C:
			p.sampleRTT(ctx)
			p.evaluate(ctx)
		}
	}
}

// sampleRTT refreshes heartbeat RTT for flow-control sessions. Legacy test
// transports do not implement the PING/PONG round trip and therefore keep a
// zero RTT without delaying the evaluator.
func (p *agentSessionPool) sampleRTT(ctx context.Context) {
	p.mu.RLock()
	entries := make([]*pooledSession, 0, len(p.sessions))
	for _, entry := range p.sessions {
		entries = append(entries, entry)
	}
	p.mu.RUnlock()
	if len(entries) == 0 {
		return
	}

	timeout := 2 * time.Second
	if interval := p.options.Config.EvaluationInterval; interval > 0 && interval < timeout {
		timeout = interval
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, entry := range entries {
		if !entry.session.OpenMode().supportsFlowControl() {
			continue
		}
		wg.Add(1)
		go func(entry *pooledSession) {
			defer wg.Done()
			if rtt, err := entry.session.Ping(pingCtx); err == nil {
				entry.lastRTT.Store(rtt.Nanoseconds())
			}
		}(entry)
	}
	wg.Wait()
}

func (p *agentSessionPool) startSlot(ctx context.Context) {
	slotCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	slotID := p.nextSlot
	p.nextSlot++
	p.slots[slotID] = &sessionPoolSlot{id: slotID, cancel: cancel}
	slotCount := len(p.slots)
	p.mu.Unlock()

	// Keep at least Min slots even while a reconnect is being established.
	// Otherwise scale-down can remove every live slot before its replacement
	// session registers, leaving the Agent temporarily unavailable.
	if slotCount > p.options.Config.Max {
		cancel()
		return
	}

	runner := p.options.Runner
	if runner == nil {
		runner = RunWebSocketWithOptions
	}
	runOptions := p.options.WebSocket
	if p.options.Metadata.InstanceID != "" {
		metadata := cloneClientMetadataOptions(p.options.Metadata)
		metadata.ConnectionSlot = slotID + 1
		runOptions.Metadata = &metadata
		runOptions.ConnectionSlot = slotID + 1
	}

	go func() {
		err := runner(slotCtx, p.options.ServerURL, p.options.Token, func(session *Session) error {
			return p.registerSession(slotCtx, slotID, session)
		}, runOptions)
		p.removeSlot(slotID)
		// slotCtx is canceled on scale-down or manager shutdown; those are not
		// terminal pool failures. Only an error while the pool is still active
		// should stop the manager.
		p.mu.RLock()
		runCtx := p.runCtx
		p.mu.RUnlock()
		if err != nil && runCtx != nil && runCtx.Err() == nil {
			select {
			case p.errCh <- err:
			default:
			}
		}
		cancel()
	}()
}

func (p *agentSessionPool) evaluate(ctx context.Context) {
	p.mu.RLock()
	slotCount := len(p.slots)
	sessionCount := len(p.sessions)
	allHigh := sessionCount > 0
	allLow := sessionCount > 0
	var scaleDownSlot *sessionPoolSlot
	for _, session := range p.sessions {
		if session.active < int64(p.options.Config.HighWatermark) {
			allHigh = false
		}
		if session.active > int64(p.options.Config.LowWatermark) {
			allLow = false
		}
	}
	for id, slot := range p.slots {
		if scaleDownSlot == nil || id > scaleDownSlot.id {
			scaleDownSlot = slot
		}
	}
	p.mu.RUnlock()

	shouldScaleUp := allHigh && slotCount < p.options.Config.Max
	shouldScaleDown := allLow && slotCount > p.options.Config.Min
	if !shouldScaleUp && !shouldScaleDown {
		return
	}
	if !p.reserveScaleDecision() {
		return
	}
	if shouldScaleUp {
		p.startSlot(ctx)
		return
	}
	if scaleDownSlot != nil {
		scaleDownSlot.cancel()
	}
}

func (p *agentSessionPool) reserveScaleDecision() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastScale.IsZero() && time.Since(p.lastScale) < p.options.Config.Cooldown {
		return false
	}
	p.lastScale = time.Now()
	return true
}

func (p *agentSessionPool) registerSession(ctx context.Context, slotID int, session *Session) error {
	if session == nil {
		return ErrSessionClosed
	}
	p.mu.Lock()
	if _, exists := p.slots[slotID]; !exists {
		p.mu.Unlock()
		return ErrSessionClosed
	}
	// Keep the pooled entry pointer so an old session's shutdown watcher cannot
	// delete a newer session that reuses the same logical slot after reconnect.
	entry := &pooledSession{slotID: slotID, session: session}
	p.sessions[slotID] = entry
	p.mu.Unlock()

	go func() {
		_ = session.Wait(ctx)
		_ = session.Close()
		p.mu.Lock()
		if current, exists := p.sessions[slotID]; exists && current == entry {
			delete(p.sessions, slotID)
		}
		p.mu.Unlock()
	}()
	return nil
}

func (p *agentSessionPool) removeSlot(slotID int) {
	p.mu.Lock()
	slot := p.slots[slotID]
	delete(p.slots, slotID)
	delete(p.sessions, slotID)
	p.mu.Unlock()
	if slot != nil {
		slot.cancel()
	}
}

func (p *agentSessionPool) cancelAll() {
	p.mu.Lock()
	slots := make([]*sessionPoolSlot, 0, len(p.slots))
	for _, slot := range p.slots {
		slots = append(slots, slot)
	}
	p.mu.Unlock()
	for _, slot := range slots {
		slot.cancel()
	}
}

func (p *agentSessionPool) slotCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.slots)
}

func (p *agentSessionPool) activeStreams() []int64 {
	p.mu.RLock()
	ids := make([]int, 0, len(p.sessions))
	for id := range p.sessions {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	active := make([]int64, 0, len(ids))
	for _, id := range ids {
		active = append(active, p.sessions[id].active)
	}
	p.mu.RUnlock()
	return active
}

func (p *agentSessionPool) acquire(request StreamRequest) (*pooledSession, error) {
	if strings.TrimSpace(request.AgentID) == "" {
		request.AgentID = p.agentID
	}
	if request.AgentID != p.agentID {
		return nil, errors.New("client: stream agent does not match session pool")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	var selected *pooledSession
	for _, session := range p.sessions {
		if selected == nil || session.active < selected.active ||
			(session.active == selected.active && session.lastRTT.Load() < selected.lastRTT.Load()) {
			selected = session
		}
	}
	if selected == nil {
		return nil, ErrAgentSessionUnavailable
	}
	selected.active++
	return selected, nil
}

func (p *agentSessionPool) release(session *pooledSession) {
	if session == nil {
		return
	}
	p.mu.Lock()
	if current, exists := p.sessions[session.slotID]; exists && current == session && current.active > 0 {
		current.active--
	}
	p.mu.Unlock()
}

type PooledStreamOpener struct {
	pool *agentSessionPool
}

func (o PooledStreamOpener) OpenStream(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, error) {
	session, err := o.pool.acquire(request)
	if err != nil {
		return nil, err
	}
	stream, err := NewSessionOpener(session.session).OpenStream(ctx, request)
	if err != nil || stream == nil {
		o.pool.release(session)
		return nil, err
	}
	return &pooledStream{stream: stream, pool: o.pool, session: session}, nil
}

func (o PooledStreamOpener) OpenStreamResult(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, protocol.OpenResultPayload, error) {
	session, err := o.pool.acquire(request)
	if err != nil {
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), err
	}
	stream, result, err := NewSessionOpener(session.session).OpenStreamResult(ctx, request)
	if err != nil || stream == nil {
		o.pool.release(session)
		if err == nil {
			err = ErrAgentSessionUnavailable
		}
		return nil, result, err
	}
	return &pooledStream{stream: stream, pool: o.pool, session: session}, result, nil
}

type pooledStream struct {
	stream  io.ReadWriteCloser
	pool    *agentSessionPool
	session *pooledSession
	once    sync.Once
}

func (s *pooledStream) Read(p []byte) (int, error)  { return s.stream.Read(p) }
func (s *pooledStream) Write(p []byte) (int, error) { return s.stream.Write(p) }

func (s *pooledStream) Close() error {
	var err error
	s.once.Do(func() {
		err = s.stream.Close()
		s.pool.release(s.session)
	})
	return err
}
