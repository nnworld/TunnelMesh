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
	// serveErr carries the reason Serve stopped, if it stopped for one. It is
	// buffered by one and written once, so a caller that is not watching cannot
	// wedge the accept loop.
	serveErr chan error
}

// ListenTCP binds addr and prepares the accept loop. The listener is created here
// rather than in Serve so a bind failure is reported to the caller synchronously.
func ListenTCP(ctx context.Context, addr string, handler func(net.Conn)) (*TCPListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return newTCPListener(ln, ctx, handler)
}

// newTCPListener is the seam both ListenTCP and the tests use, so a listener whose
// Accept always fails can be injected without opening a real socket.
func newTCPListener(ln net.Listener, ctx context.Context, handler func(net.Conn)) (*TCPListener, error) {
	l := &TCPListener{listener: ln, serve: handler, serveErr: make(chan error, 1)}
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

// Err reports the channel that receives the accept-loop failure, if there is one.
// A closed channel means the listener stopped cleanly; nil was never published.
//
// Without this the goroutine that runs Serve could die on a permanent Accept error
// - an exhausted descriptor limit is the common one - and the process would keep
// reporting a tunnel that is bound but deaf, which is the failure mode that costs
// the most time to diagnose from the outside.
func (l *TCPListener) Err() <-chan error {
	if l == nil {
		return nil
	}
	return l.serveErr
}

func (l *TCPListener) Serve() error {
	if l == nil || l.listener == nil {
		return ErrListenerClosed
	}
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				close(l.publish(nil))
				return nil
			}
			l.publish(err)
			return err
		}
		if l.serve != nil {
			go l.serve(conn)
		} else {
			_ = conn.Close()
		}
	}
}

// publish records why the accept loop stopped and returns the channel so a clean
// shutdown can close it. It never blocks: the buffer is the contract.
func (l *TCPListener) publish(err error) chan error {
	select {
	case l.serveErr <- err:
	default:
	}
	return l.serveErr
}

func (l *TCPListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	var err error
	l.close.Do(func() { err = l.listener.Close() })
	return err
}
