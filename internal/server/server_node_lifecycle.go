package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	defaultServerNodeHeartbeatInterval = 30 * time.Second
	defaultServerNodeHeartbeatTTL      = 90 * time.Second
	serverNodeOperationTimeout         = 5 * time.Second
)

// ServerNodeLifecycle keeps the local Server's inventory row online. Runtime
// registration deliberately preserves administrator lifecycle and epoch state.
type ServerNodeLifecycle struct {
	nodes    storage.NodeRepository
	nodeID   string
	address  string
	interval time.Duration
	ttl      time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewServerNodeLifecycle(db *storage.DB, nodeID, address string, interval, ttl time.Duration) *ServerNodeLifecycle {
	var nodes storage.NodeRepository
	if db != nil {
		nodes = db.Nodes()
	}
	return &ServerNodeLifecycle{
		nodes: nodes, nodeID: nodeID, address: address,
		interval: interval, ttl: ttl,
	}
}

// Start registers the node and begins heartbeat updates. It is idempotent so
// runtime constructors and tests can safely retry after a transient DB error.
func (l *ServerNodeLifecycle) Start(ctx context.Context) error {
	if l == nil || l.nodes == nil || l.nodeID == "" {
		return errors.New("server node lifecycle is not configured")
	}
	if l.interval <= 0 || l.ttl <= 0 {
		return errors.New("server node heartbeat interval and TTL must be positive")
	}

	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	l.cancel, l.done = cancel, done
	l.mu.Unlock()

	if err := l.register(ctx); err != nil {
		l.mu.Lock()
		l.cancel, l.done = nil, nil
		l.mu.Unlock()
		cancel()
		close(done)
		return err
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(l.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := l.heartbeat(runCtx); err != nil {
					slog.WarnContext(runCtx, "server node heartbeat failed", "node_id", l.nodeID, "error", err)
				}
			}
		}
	}()
	return nil
}

// Close stops heartbeat and waits for the worker to exit.
func (l *ServerNodeLifecycle) Close() error {
	if l == nil {
		return nil
	}
	l.stop()
	return nil
}

func (l *ServerNodeLifecycle) register(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, serverNodeOperationTimeout)
	defer cancel()

	now := time.Now().UTC()
	expires := l.expiry(now)
	node := storage.ServerNode{
		ID: l.nodeID, Name: l.nodeID, Address: l.address, Epoch: 1,
		Metadata: "{}", LastSeenAt: &now, ExpiresAt: &expires,
	}
	if err := l.nodes.Ensure(ctx, node); err != nil {
		return err
	}
	found, err := l.nodes.Get(ctx, l.nodeID)
	if err != nil {
		return err
	}
	if found.DeletedAt != nil {
		return fmt.Errorf("server node %s is deleted", l.nodeID)
	}
	if !found.Enabled {
		return fmt.Errorf("server node %s is disabled", l.nodeID)
	}
	return l.nodes.Touch(ctx, l.nodeID, now, l.expiry(now))
}

func (l *ServerNodeLifecycle) heartbeat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, serverNodeOperationTimeout)
	defer cancel()
	now := time.Now().UTC()
	return l.nodes.Touch(ctx, l.nodeID, now, l.expiry(now))
}

func (l *ServerNodeLifecycle) expiry(now time.Time) time.Time {
	return now.Add(l.ttl)
}

func (l *ServerNodeLifecycle) stop() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
