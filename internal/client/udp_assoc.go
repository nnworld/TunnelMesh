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

type UDPAssociationConfig struct {
	AgentID     string
	TargetHost  string
	TargetPort  int
	IdleTimeout time.Duration
	MaxDatagram int
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
	return &UDPAssociationManager{opener: opener, cfg: cfg, items: make(map[string]*udpAssociation)}, nil
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
		candidate := &udpAssociation{key: key, addr: cloneUDPAddr(source), stream: ds, lastUsed: time.Now()}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			candidate.Close()
			return ErrListenerClosed
		}
		if current := m.items[key]; current != nil {
			m.mu.Unlock()
			candidate.Close()
			a = current
		} else {
			m.items[key] = candidate
			m.mu.Unlock()
			a = candidate
			go m.readBack(a)
		}
	}
	a.touch(time.Now())
	return a.stream.WriteDatagram(append([]byte(nil), payload...))
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
		a.touch(time.Now())
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
