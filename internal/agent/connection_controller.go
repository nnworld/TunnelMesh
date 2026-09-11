package agent

import (
	"context"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
)

// ConnectionRunner owns one physical WebSocket and its stream dispatcher. It
// must return when the supplied context is cancelled.
type ConnectionRunner func(ctx context.Context, connectionID string, connectionEpoch int64) error

// ConnectionStats is the health input used by the adaptive controller. Values
// are process-local and never persisted by the controller itself.
type ConnectionStats struct {
	ConnectionPoolSupported bool
	ActiveStreams           int64
	QueuePressure           int
	RTT                     time.Duration
	Errors                  int64
	PendingDials            int
	OpenP95                 time.Duration
	TTFBP95                 time.Duration
	WriterQueueWait         time.Duration
}

type ConnectionPoolSnapshot struct {
	Active             int
	Min                int
	Max                int
	MaxActiveStreams   int64
	MaxQueuePressure   int
	MaxRTT             time.Duration
	MaxPendingDials    int
	MaxOpenP95         time.Duration
	MaxTTFBP95         time.Duration
	MaxWriterQueueWait time.Duration
	LastScaleAt        time.Time
	Closed             bool
}

type ConnectionControllerOptions struct {
	AgentID             string
	InstanceID          string
	Metrics             *observability.Metrics
	Min                 int
	Max                 int
	HighWatermark       int
	LowWatermark        int
	EvaluationInterval  time.Duration
	Cooldown            time.Duration
	HighRTT             time.Duration
	HighPendingDials    int
	HighOpenP95         time.Duration
	HighTTFBP95         time.Duration
	HighWriterQueueWait time.Duration
	Runner              ConnectionRunner
}

type managedConnection struct {
	id     string
	epoch  int64
	cancel context.CancelFunc
}

type ConnectionController struct {
	options     ConnectionControllerOptions
	mu          sync.Mutex
	connections map[string]*managedConnection
	stats       map[string]ConnectionStats
	highStreak  int
	lowStreak   int
	nextNumber  int
	lastScaleAt time.Time
	closed      bool
	wg          sync.WaitGroup
}

func NewConnectionController(options ConnectionControllerOptions) *ConnectionController {
	if options.Min <= 0 {
		options.Min = 1
	}
	if options.Metrics != nil {
		options.Metrics.SetAgentConnectionCapacity(options.AgentID, options.InstanceID, options.Max)
	}
	if options.Max < options.Min {
		options.Max = options.Min
	}
	if options.HighWatermark <= 0 {
		options.HighWatermark = 16
	}
	if options.LowWatermark < 0 {
		options.LowWatermark = 2
	}
	if options.EvaluationInterval <= 0 {
		options.EvaluationInterval = 10 * time.Second
	}
	if options.Cooldown <= 0 {
		options.Cooldown = 30 * time.Second
	}
	if options.HighRTT <= 0 {
		options.HighRTT = 500 * time.Millisecond
	}
	if options.HighPendingDials <= 0 {
		options.HighPendingDials = 1
	}
	if options.HighOpenP95 <= 0 {
		options.HighOpenP95 = time.Second
	}
	if options.HighTTFBP95 <= 0 {
		options.HighTTFBP95 = time.Second
	}
	if options.HighWriterQueueWait <= 0 {
		options.HighWriterQueueWait = 100 * time.Millisecond
	}
	return &ConnectionController{
		options: options, connections: make(map[string]*managedConnection),
		stats: make(map[string]ConnectionStats),
	}
}

func (c *ConnectionController) Run(ctx context.Context) error {
	if c == nil || c.options.Runner == nil {
		return context.Canceled
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return context.Canceled
	}
	for i := 0; i < c.options.Min; i++ {
		c.startLocked(ctx)
	}
	c.mu.Unlock()

	ticker := time.NewTicker(c.options.EvaluationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.closeAll()
			c.wg.Wait()
			return ctx.Err()
		case <-ticker.C:
			c.evaluate(time.Now().UTC())
		}
	}
}

func (c *ConnectionController) Report(connectionID string, stats ConnectionStats) {
	if c == nil || connectionID == "" {
		return
	}
	c.mu.Lock()
	c.stats[connectionID] = stats
	if c.options.Metrics != nil {
		c.options.Metrics.ObserveAgentActiveStreams(c.options.AgentID, c.options.InstanceID, connectionID, int(stats.ActiveStreams))
		if stats.RTT >= 0 {
			c.options.Metrics.ObserveAgentConnectionRTT(c.options.AgentID, c.options.InstanceID, connectionID, stats.RTT)
		}
	}
	c.mu.Unlock()
}

func (c *ConnectionController) Snapshot() ConnectionPoolSnapshot {
	if c == nil {
		return ConnectionPoolSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := ConnectionPoolSnapshot{
		Active: len(c.connections), Min: c.options.Min, Max: c.options.Max,
		MaxQueuePressure: -1, LastScaleAt: c.lastScaleAt, Closed: c.closed,
	}
	for _, stats := range c.stats {
		if stats.ActiveStreams > snapshot.MaxActiveStreams {
			snapshot.MaxActiveStreams = stats.ActiveStreams
		}
		if stats.QueuePressure > snapshot.MaxQueuePressure {
			snapshot.MaxQueuePressure = stats.QueuePressure
		}
		if stats.RTT > snapshot.MaxRTT {
			snapshot.MaxRTT = stats.RTT
		}
		if stats.PendingDials > snapshot.MaxPendingDials {
			snapshot.MaxPendingDials = stats.PendingDials
		}
		if stats.OpenP95 > snapshot.MaxOpenP95 {
			snapshot.MaxOpenP95 = stats.OpenP95
		}
		if stats.TTFBP95 > snapshot.MaxTTFBP95 {
			snapshot.MaxTTFBP95 = stats.TTFBP95
		}
		if stats.WriterQueueWait > snapshot.MaxWriterQueueWait {
			snapshot.MaxWriterQueueWait = stats.WriterQueueWait
		}
	}
	return snapshot
}

func (c *ConnectionController) startLocked(ctx context.Context) {
	if len(c.connections) >= c.options.Max {
		return
	}
	c.nextNumber++
	id := "conn-" + itoa(c.nextNumber)
	connectionCtx, cancel := context.WithCancel(ctx)
	connection := &managedConnection{id: id, epoch: 1, cancel: cancel}
	c.connections[id] = connection
	if c.options.Metrics != nil {
		c.options.Metrics.ObserveAgentConnection(c.options.AgentID, c.options.InstanceID, id, true)
	}
	c.wg.Add(1)
	go c.supervise(connectionCtx, connection)
}

func (c *ConnectionController) supervise(ctx context.Context, connection *managedConnection) {
	defer c.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = c.options.Runner(ctx, connection.id, connection.epoch)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *ConnectionController) evaluate(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.options.Runner == nil {
		return
	}
	serverSupported := false
	high := false
	low := true
	for _, stats := range c.stats {
		serverSupported = serverSupported || stats.ConnectionPoolSupported
		if stats.ActiveStreams >= int64(c.options.HighWatermark) ||
			stats.QueuePressure >= 80 || stats.RTT >= c.options.HighRTT ||
			stats.PendingDials >= c.options.HighPendingDials ||
			stats.OpenP95 >= c.options.HighOpenP95 ||
			stats.TTFBP95 >= c.options.HighTTFBP95 ||
			stats.WriterQueueWait >= c.options.HighWriterQueueWait {
			high = true
		}
		if stats.ActiveStreams > int64(c.options.LowWatermark) {
			low = false
		}
	}
	if high && serverSupported {
		c.highStreak++
	} else {
		c.highStreak = 0
	}
	if low {
		c.lowStreak++
	} else {
		c.lowStreak = 0
	}
	if c.highStreak >= 2 && len(c.connections) < c.options.Max {
		c.startLocked(context.Background())
		c.lastScaleAt, c.highStreak, c.lowStreak = now, 0, 0
		if c.options.Metrics != nil {
			c.options.Metrics.ObserveAgentScaleDecision(c.options.AgentID, c.options.InstanceID, "scale-up", "high-load")
		}
		return
	}
	if c.lowStreak >= 2 && len(c.connections) > c.options.Min &&
		(c.lastScaleAt.IsZero() || now.Sub(c.lastScaleAt) >= c.options.Cooldown) {
		c.scaleDownLocked()
		c.lastScaleAt, c.highStreak, c.lowStreak = now, 0, 0
		if c.options.Metrics != nil {
			c.options.Metrics.ObserveAgentScaleDecision(c.options.AgentID, c.options.InstanceID, "scale-down", "low-load")
		}
	}
}

func (c *ConnectionController) scaleDownLocked() {
	var victim *managedConnection
	for _, connection := range c.connections {
		if victim == nil || connection.id > victim.id {
			victim = connection
		}
	}
	if victim == nil {
		return
	}
	delete(c.connections, victim.id)
	delete(c.stats, victim.id)
	if c.options.Metrics != nil {
		c.options.Metrics.ObserveAgentConnection(c.options.AgentID, c.options.InstanceID, victim.id, false)
	}
	victim.cancel()
}

func (c *ConnectionController) closeAll() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for _, connection := range c.connections {
		if c.options.Metrics != nil {
			c.options.Metrics.ObserveAgentConnection(c.options.AgentID, c.options.InstanceID, connection.id, false)
		}
		connection.cancel()
	}
	c.connections = make(map[string]*managedConnection)
	c.mu.Unlock()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
