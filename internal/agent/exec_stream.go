package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// execStream is one script run presented as a byte stream, so the dispatcher's
// existing flow control and close handling apply unchanged. Inbound bytes are the
// request header and then the script; outbound bytes are output records ending with an
// exit record. The script starts once all of its bytes have arrived.
type execStream struct {
	runner *execRunner

	mu      sync.Mutex
	inbound []byte
	started bool
	closed  bool
	cancel  context.CancelFunc

	pr *io.PipeReader
	pw *io.PipeWriter
}

func newExecStream(runner *execRunner) *execStream {
	pr, pw := io.Pipe()
	return &execStream{runner: runner, pr: pr, pw: pw}
}

// Write buffers the request. It never blocks on the script itself, so a slow run
// cannot stall the peer's inbound pump.
func (s *execStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if s.started {
		return 0, errors.New("agent: exec stream accepts no bytes after the script starts")
	}
	s.inbound = append(s.inbound, p...)
	header, consumed, err := protocol.ParseExecHeader(s.inbound)
	if err != nil {
		return 0, err
	}
	if consumed == 0 {
		return len(p), nil
	}
	cfg := s.runner.Config()
	if header.ScriptBytes > cfg.MaxScriptBytes {
		return 0, ErrExecScriptTooLarge
	}
	total := consumed + header.ScriptBytes
	if len(s.inbound) < total {
		return len(p), nil
	}
	if len(s.inbound) > total {
		return 0, errors.New("agent: exec stream carries bytes beyond the declared script")
	}
	script := append([]byte(nil), s.inbound[consumed:total]...)
	s.inbound = nil
	s.started = true
	timeout := time.Duration(header.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.run(ctx, script, timeout)
	return len(p), nil
}

// run executes the script and streams its records. Output writers serialise through
// the shared limit inside Run, so each record is one write and never interleaves.
func (s *execStream) run(ctx context.Context, script []byte, timeout time.Duration) {
	stdout := recordWriter{w: s.pw, kind: protocol.ExecRecordStdout}
	stderr := recordWriter{w: s.pw, kind: protocol.ExecRecordStderr}
	result, err := s.runner.Run(ctx, script, timeout, stdout, stderr)
	if err != nil {
		_ = protocol.WriteExecRecord(s.pw, protocol.ExecRecordError, []byte(err.Error()))
		_ = s.pw.Close()
		return
	}
	exit, marshalErr := json.Marshal(protocol.ExecExit{
		ExitCode: result.ExitCode, Signaled: result.Signaled, TimedOut: result.TimedOut,
		Truncated: result.Truncated, DurationMS: result.Duration.Milliseconds(),
	})
	if marshalErr == nil {
		_ = protocol.WriteExecRecord(s.pw, protocol.ExecRecordExit, exit)
	}
	_ = s.pw.Close()
}

// Read returns output records as they are produced.
func (s *execStream) Read(p []byte) (int, error) { return s.pr.Read(p) }

// Close ends the run. A closed stream cancels the script, which kills its process
// group, so an abandoned browser session cannot leave a process running.
func (s *execStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = s.pw.CloseWithError(io.ErrClosedPipe)
	return s.pr.Close()
}

// recordWriter frames each write as one record of a fixed kind.
type recordWriter struct {
	w    io.Writer
	kind byte
}

func (r recordWriter) Write(p []byte) (int, error) {
	for len(p) > 0 {
		chunk := p
		if len(chunk) > protocol.MaxExecRecordPayload {
			chunk = chunk[:protocol.MaxExecRecordPayload]
		}
		if err := protocol.WriteExecRecord(r.w, r.kind, chunk); err != nil {
			return 0, fmt.Errorf("agent: write exec record: %w", err)
		}
		p = p[len(chunk):]
	}
	return len(p), nil
}
