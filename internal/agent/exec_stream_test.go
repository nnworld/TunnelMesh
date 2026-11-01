package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func execRequest(t *testing.T, timeoutS int, script string) []byte {
	t.Helper()
	header, err := protocol.EncodeExecHeader(protocol.ExecHeader{TimeoutSeconds: timeoutS, ScriptBytes: len(script)})
	if err != nil {
		t.Fatal(err)
	}
	return append(header, []byte(script)...)
}

// readRecords drains the stream into per-kind buffers and returns the exit record.
func readRecords(t *testing.T, stream io.Reader) (stdout, stderr string, exit protocol.ExecExit, sawExit bool) {
	t.Helper()
	var out, errBuf bytes.Buffer
	for {
		kind, payload, err := protocol.ReadExecRecord(stream)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
			break
		}
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		switch kind {
		case protocol.ExecRecordStdout:
			out.Write(payload)
		case protocol.ExecRecordStderr:
			errBuf.Write(payload)
		case protocol.ExecRecordExit:
			sawExit = true
			if err := json.Unmarshal(payload, &exit); err != nil {
				t.Fatalf("exit payload: %v", err)
			}
		case protocol.ExecRecordError:
			t.Fatalf("run failed: %s", payload)
		}
	}
	return out.String(), errBuf.String(), exit, sawExit
}

func TestExecStreamRunsScriptAndReportsExit(t *testing.T) {
	skipWithoutPOSIXShell(t)
	stream := newExecStream(testExecRunner(ExecConfig{}))
	defer stream.Close()
	if _, err := stream.Write(execRequest(t, 10, "echo hello; echo oops 1>&2; exit 7")); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdout, stderr, exit, sawExit := readRecords(t, stream)
	if !sawExit || exit.ExitCode != 7 || exit.TimedOut {
		t.Fatalf("exit = %+v saw=%v", exit, sawExit)
	}
	if stdout != "hello\n" || stderr != "oops\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestExecStreamAcceptsTheRequestInPieces(t *testing.T) {
	skipWithoutPOSIXShell(t)
	stream := newExecStream(testExecRunner(ExecConfig{}))
	defer stream.Close()
	request := execRequest(t, 10, "echo split")
	for i := 0; i < len(request); i++ {
		if _, err := stream.Write(request[i : i+1]); err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
	stdout, _, _, sawExit := readRecords(t, stream)
	if !sawExit || stdout != "split\n" {
		t.Fatalf("stdout=%q saw=%v", stdout, sawExit)
	}
}

func TestExecStreamRefusesOversizedScriptFromHeader(t *testing.T) {
	stream := newExecStream(testExecRunner(ExecConfig{MaxScriptBytes: 8}))
	defer stream.Close()
	header, _ := protocol.EncodeExecHeader(protocol.ExecHeader{TimeoutSeconds: 1, ScriptBytes: 9})
	if _, err := stream.Write(header); !errors.Is(err, ErrExecScriptTooLarge) {
		t.Fatalf("err = %v, want ErrExecScriptTooLarge", err)
	}
}

func TestExecStreamRefusesBytesAfterTheScriptStarts(t *testing.T) {
	skipWithoutPOSIXShell(t)
	stream := newExecStream(testExecRunner(ExecConfig{}))
	defer stream.Close()
	if _, err := stream.Write(execRequest(t, 10, "sleep 1")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("extra")); err == nil {
		t.Fatal("bytes after the script started must be refused")
	}
}

// Closing the stream must end the process group, not leave the script running.
func TestExecStreamCloseKillsTheScript(t *testing.T) {
	skipWithoutPOSIXShell(t)
	stream := newExecStream(testExecRunner(ExecConfig{}))
	if _, err := stream.Write(execRequest(t, 60, "sleep 60")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := stream.Write([]byte("x")); err == nil {
		t.Fatal("a closed stream must refuse writes")
	}
}
