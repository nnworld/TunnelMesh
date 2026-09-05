package client

import (
	"context"
	"errors"
	"net"
	"sync"
)

var ErrListenerClosed = errors.New("client listener closed")

// TCPListener owns the local socket lifecycle and delegates accepted
// connections to a bounded handler. It deliberately closes the listener on
// context cancellation so shutdown does not depend on a new incoming client.
type TCPListener struct {
	listener net.Listener
	serve    func(net.Conn)
	close    sync.Once
}

func ListenTCP(ctx context.Context, addr string, handler func(net.Conn)) (*TCPListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	l := &TCPListener{listener: ln, serve: handler}
	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = l.Close()
		}()
	}
	return l, nil
}

func (l *TCPListener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}
func (l *TCPListener) Serve() error {
	if l == nil || l.listener == nil {
		return ErrListenerClosed
	}
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if l.serve != nil {
			go l.serve(conn)
		} else {
			_ = conn.Close()
		}
	}
}
func (l *TCPListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	var err error
	l.close.Do(func() { err = l.listener.Close() })
	return err
}
