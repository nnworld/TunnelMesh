package client

import (
	"context"
	"errors"
	"io"
)

// ProxyStdio connects a command's stdin/stdout to a raw logical stream. The
// two copy loops run independently so SSH ProxyCommand and websocat can carry
// full-duplex bytes without line buffering or base64 transformations.
func ProxyStdio(ctx context.Context, in io.Reader, out io.Writer, stream io.ReadWriteCloser) error {
	if stream == nil || in == nil || out == nil {
		return errors.New("client: nil stdio proxy endpoint")
	}
	result := make(chan error, 2)
	go func() {
		_, err := io.CopyBuffer(stream, in, make([]byte, 32<<10))
		result <- err
	}()
	go func() {
		_, err := io.CopyBuffer(out, stream, make([]byte, 32<<10))
		result <- err
	}()
	var firstErr error
	for completed := 0; completed < 2; completed++ {
		select {
		case err := <-result:
			if firstErr == nil && !errors.Is(err, io.EOF) {
				firstErr = err
			}
		case <-ctx.Done():
			_ = stream.Close()
			return ctx.Err()
		}
	}
	_ = stream.Close()
	return firstErr
}
