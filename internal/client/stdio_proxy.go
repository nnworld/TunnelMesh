package client

import (
	"context"
	"errors"
	"io"
	"time"
)

// ProxyStdio connects a command's stdin/stdout to a raw logical stream. The
// two copy loops run independently so SSH ProxyCommand and websocat can carry
// full-duplex bytes without line buffering or base64 transformations.
func ProxyStdio(ctx context.Context, in io.Reader, out io.Writer, stream io.ReadWriteCloser) error {
	if stream == nil || in == nil || out == nil {
		return errors.New("client: nil stdio proxy endpoint")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan error, 2)
	go func() {
		_, err := io.CopyBuffer(stream, in, make([]byte, 32<<10))
		if c, ok := stream.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		result <- err
	}()
	go func() {
		_, err := io.CopyBuffer(out, stream, make([]byte, 32<<10))
		result <- err
	}()
	var firstErr error
	select {
	case firstErr = <-result:
	case <-ctx.Done():
		_ = stream.Close()
		return ctx.Err()
	}
	if firstErr != nil && !errors.Is(firstErr, io.EOF) {
		_ = stream.Close()
	}
	select {
	case err := <-result:
		if errors.Is(firstErr, io.EOF) || !errors.Is(err, io.EOF) {
			firstErr = err
		}
	case <-ctx.Done():
		_ = stream.Close()
		return ctx.Err()
	case <-time.After(time.Second):
		_ = stream.Close()
	}
	_ = stream.Close()
	if errors.Is(firstErr, io.EOF) {
		return nil
	}
	return firstErr
}
