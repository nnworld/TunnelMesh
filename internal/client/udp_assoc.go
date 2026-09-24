package client

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

type DatagramStream interface {
	io.Closer
	WriteDatagram([]byte) error
	ReadDatagram() ([]byte, error)
}

// DatagramOpener is the message-oriented counterpart to StreamOpener. A
// WebSocket/FrameData implementation must use this interface for UDP so one
// binary message remains exactly one datagram; it must not add a byte-stream
// length prefix.
type DatagramOpener interface {
	OpenDatagram(context.Context, StreamRequest) (DatagramStream, error)
}

// defaultUDPAssociationLimit bounds how many tunnel streams one local UDP
// forward may hold. Every association is a source address that cost a dial and
// a stream, so an unbounded table lets any local process exhaust the Agent dial
// pool. It is a local policy, not a protocol change.
const defaultUDPAssociationLimit = 1024

// ErrTooManyUDPAssociations reports that the table is full and every entry is
// as recent as the packet that wanted to join it. Dropping a datagram is the
// correct UDP answer; growing the table is not.
var ErrTooManyUDPAssociations = errors.New("client: too many UDP associations")

type UDPAssociationConfig struct {
	AgentID         string
	TargetHost      string
	TargetPort      int
	IdleTimeout     time.Duration
	MaxDatagram     int
	MaxAssociations int
}

type udpAssociation struct {
	key      string
	addr     *net.UDPAddr
	stream   DatagramStream
	close    sync.Once
	mu       sync.Mutex
	lastUsed time.Time
}

func (a *udpAssociation) touch(now time.Time)  { a.mu.Lock(); a.lastUsed = now; a.mu.Unlock() }
func (a *udpAssociation) idleSince() time.Time { a.mu.Lock(); defer a.mu.Unlock(); return a.lastUsed }
func (a *udpAssociation) Close()               { a.close.Do(func() { _ = a.stream.Close() }) }

type UDPAssociationManager struct {
	opener  StreamOpener
	cfg     UDPAssociationConfig
	mu      sync.Mutex
	items   map[string]*udpAssociation
	deliver func(*net.UDPAddr, []byte)
	closed  bool
	now     func() time.Time
}

func NewUDPAssociationManager(opener StreamOpener, cfg UDPAssociationConfig) (*UDPAssociationManager, error) {
	if opener == nil {
		return nil, errors.New("client: nil stream opener")
	}
	if strings.TrimSpace(cfg.TargetHost) == "" || cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
		return nil, errors.New("client: invalid UDP target")
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 2 * time.Minute
	}
	if cfg.MaxDatagram <= 0 {
		cfg.MaxDatagram = 64 << 10
	}
	if cfg.MaxAssociations <= 0 {
		cfg.MaxAssociations = defaultUDPAssociationLimit
	}
	return &UDPAssociationManager{opener: opener, cfg: cfg, items: make(map[string]*udpAssociation), now: time.Now}, nil
}
func (m *UDPAssociationManager) Len() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.items) }
func (m *UDPAssociationManager) HandleDatagram(ctx context.Context, source *net.UDPAddr, payload []byte) error {
	if source == nil {
		return errors.New("client: nil UDP source")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrListenerClosed
	}
	m.mu.Unlock()
	if len(payload) > m.cfg.MaxDatagram {
		return errors.New("client: UDP datagram too large")
	}
	key := source.String()
	m.mu.Lock()
	a := m.items[key]
	m.mu.Unlock()
	if a == nil {
		m.mu.Lock()
		refused := !m.hasRoomLocked()
		m.mu.Unlock()
		if refused {
			return ErrTooManyUDPAssociations
		}
		req := StreamRequest{AgentID: m.cfg.AgentID, Protocol: "udp", TargetHost: m.cfg.TargetHost, TargetPort: m.cfg.TargetPort}
		var ds DatagramStream
		var err error
		if opener, ok := m.opener.(DatagramOpener); ok {
			ds, err = opener.OpenDatagram(ctx, req)
		} else {
			var stream io.ReadWriteCloser
			stream, err = m.opener.OpenStream(ctx, req)
			if err == nil {
				ds, _ = stream.(DatagramStream)
				if ds == nil {
					_ = stream.Close()
					err = errors.New("client: UDP opener does not preserve datagram boundaries")
				}
			}
		}
		if err != nil {
			return err
		}
		candidate := &udpAssociation{key: key, addr: cloneUDPAddr(source), stream: ds, lastUsed: m.now()}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			candidate.Close()
			return ErrListenerClosed
		}
		var evicted *udpAssociation
		switch current := m.items[key]; {
		case current != nil:
			m.mu.Unlock()
			candidate.Close()
			a = current
		default:
			if len(m.items) >= m.cfg.MaxAssociations {
				evicted = m.evictLeastRecentlyUsedLocked()
				if evicted == nil {
					m.mu.Unlock()
					candidate.Close()
					return ErrTooManyUDPAssociations
				}
			}
			m.items[key] = candidate
			m.mu.Unlock()
			a = candidate
			go m.readBack(a)
		}
		// Closing outside the lock keeps a slow stream teardown from holding up
		// every other source on this listener.
		if evicted != nil {
			evicted.Close()
		}
	}
	a.touch(m.now())
	return a.stream.WriteDatagram(append([]byte(nil), payload...))
}

// evictLeastRecentlyUsedLocked frees one slot for a new source. It returns nil
// when nothing is quieter than the freshest entry, which means every association
// is being used at the same instant rather than the table holding stale flows.
// hasRoomLocked answers the admission question without touching the table, so a
// packet that cannot get a slot never costs an Agent dial.
func (m *UDPAssociationManager) hasRoomLocked() bool {
	return len(m.items) < m.cfg.MaxAssociations || m.leastRecentlyUsedLocked() != nil
}

func (m *UDPAssociationManager) evictLeastRecentlyUsedLocked() *udpAssociation {
	victim := m.leastRecentlyUsedLocked()
	if victim == nil {
		return nil
	}
	delete(m.items, victim.key)
	return victim
}

func (m *UDPAssociationManager) leastRecentlyUsedLocked() *udpAssociation {
	var oldest, newest *udpAssociation
	for _, a := range m.items {
		if oldest == nil || a.lastUsed.Before(oldest.lastUsed) {
			oldest = a
		}
		if newest == nil || a.lastUsed.After(newest.lastUsed) {
			newest = a
		}
	}
	if oldest == nil || newest == nil || oldest.lastUsed.Equal(newest.lastUsed) {
		return nil
	}
	return oldest
}

func (m *UDPAssociationManager) readBack(a *udpAssociation) {
	for {
		payload, err := a.stream.ReadDatagram()
		if err != nil {
			m.mu.Lock()
			if current := m.items[a.key]; current == a {
				delete(m.items, a.key)
			}
			m.mu.Unlock()
			a.Close()
			return
		}
		if len(payload) > m.cfg.MaxDatagram {
			continue
		}
		a.touch(m.now())
		m.mu.Lock()
		deliver := m.deliver
		m.mu.Unlock()
		if deliver != nil {
			deliver(a.addr, payload)
		}
	}
}
func (m *UDPAssociationManager) Expire(now time.Time) int {
	if now.IsZero() {
		now = time.Now()
	}
	m.mu.Lock()
	var expired []*udpAssociation
	for key, a := range m.items {
		if now.Sub(a.idleSince()) >= m.cfg.IdleTimeout {
			delete(m.items, key)
			expired = append(expired, a)
		}
	}
	m.mu.Unlock()
	for _, a := range expired {
		a.Close()
	}
	return len(expired)
}
func (m *UDPAssociationManager) Close() error {
	m.mu.Lock()
	m.closed = true
	items := make([]*udpAssociation, 0, len(m.items))
	for key, a := range m.items {
		delete(m.items, key)
		items = append(items, a)
	}
	m.mu.Unlock()
	for _, a := range items {
		a.Close()
	}
	return nil
}

func cloneUDPAddr(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	c := *a
	c.IP = append(net.IP(nil), a.IP...)
	return &c
}
