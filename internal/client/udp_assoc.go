package client

import (
	"bufio"
	"context"
	"encoding/binary"
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
	if len(payload) > m.cfg.MaxDatagram {
		return errors.New("client: UDP datagram too large")
	}
	key := source.String()
	m.mu.Lock()
	a := m.items[key]
	if a == nil {
		stream, err := m.opener.OpenStream(ctx, StreamRequest{AgentID: m.cfg.AgentID, Protocol: "udp", TargetHost: m.cfg.TargetHost, TargetPort: m.cfg.TargetPort})
		if err != nil {
			m.mu.Unlock()
			return err
		}
		ds, ok := stream.(DatagramStream)
		if !ok {
			ds = newFramedDatagramStream(stream, m.cfg.MaxDatagram)
		}
		a = &udpAssociation{key: key, addr: cloneUDPAddr(source), stream: ds, lastUsed: time.Now()}
		m.items[key] = a
		go m.readBack(a)
	}
	a.touch(time.Now())
	m.mu.Unlock()
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
		if m.deliver != nil {
			m.deliver(a.addr, payload)
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

type framedDatagramStream struct {
	conn            io.ReadWriteCloser
	max             int
	readMu, writeMu sync.Mutex
	reader          *bufio.Reader
}

func newFramedDatagramStream(conn io.ReadWriteCloser, max int) *framedDatagramStream {
	return &framedDatagramStream{conn: conn, max: max, reader: bufio.NewReader(conn)}
}
func (s *framedDatagramStream) WriteDatagram(p []byte) error {
	if len(p) > s.max {
		return errors.New("client: UDP datagram too large")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(p)))
	if _, err := s.conn.Write(h[:]); err != nil {
		return err
	}
	_, err := s.conn.Write(p)
	return err
}
func (s *framedDatagramStream) ReadDatagram() ([]byte, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	var h [4]byte
	if _, err := io.ReadFull(s.reader, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n > uint32(s.max) {
		return nil, errors.New("client: UDP datagram too large")
	}
	p := make([]byte, n)
	_, err := io.ReadFull(s.reader, p)
	return p, err
}
func (s *framedDatagramStream) Close() error { return s.conn.Close() }
