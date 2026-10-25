package workerproto

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestReadyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := ReadyInfo{PID: 4242, Version: "v1.3.5", ServerVersion: "v1.4.0", ReadyAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	if err := WriteReady(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadReady(dir)
	if err != nil || !ok {
		t.Fatalf("ReadReady() = %+v, %v, %v", got, ok, err)
	}
	if got != want {
		t.Fatalf("ReadReady() = %+v, want %+v", got, want)
	}
}

func TestReadReadyMissingIsNotAnError(t *testing.T) {
	got, ok, err := ReadReady(t.TempDir())
	if err != nil || ok || got != (ReadyInfo{}) {
		t.Fatalf("ReadReady() = %+v, %v, %v, want zero value, false, nil", got, ok, err)
	}
}

// The supervisor polls while the worker rewrites the file after a reconnect, so a
// reader must see either the old or the new document, never a torn one.
func TestWriteReadyIsAtomicUnderConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	if err := WriteReady(dir, ReadyInfo{PID: 1, Version: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = WriteReady(dir, ReadyInfo{PID: i, Version: "v1.0.0", ServerVersion: fmt.Sprintf("v1.0.%d", i)})
		}
	}()
	for i := 0; i < 2000; i++ {
		if _, ok, err := ReadReady(dir); err != nil || !ok {
			close(stop)
			wg.Wait()
			t.Fatalf("read %d saw a torn or missing file: ok=%v err=%v", i, ok, err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestWriteReadyUsesRestrictivePermissionsAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if err := WriteReady(dir, ReadyInfo{PID: 1}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ReadyFileName {
		t.Fatalf("directory holds %v, want only %s", entries, ReadyFileName)
	}
	info, err := os.Stat(filepath.Join(dir, ReadyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtimeHasUnixModes() && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReadReadyRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ReadyFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadReady(dir); err == nil || ok {
		t.Fatalf("ReadReady() ok=%v err=%v, want an error", ok, err)
	}
}

func TestExitCodes(t *testing.T) {
	// These numbers are a contract with systemd unit files (RestartPreventExitStatus)
	// and with operators, so they must not drift silently.
	want := map[string]int{"OK": 0, "Generic": 1, "SupervisorGaveUp": 70, "LockHeld": 75, "AuthFatal": 77, "Config": 78}
	got := map[string]int{"OK": ExitOK, "Generic": ExitGeneric, "SupervisorGaveUp": ExitSupervisorGaveUp, "LockHeld": ExitLockHeld, "AuthFatal": ExitAuthFatal, "Config": ExitConfig}
	for name, code := range want {
		if got[name] != code {
			t.Errorf("Exit%s = %d, want %d", name, got[name], code)
		}
	}
}

func TestExitCodeOf(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain error", base, ExitGeneric},
		{"typed", &ExitError{Code: ExitConfig, Err: base}, ExitConfig},
		{"wrapped typed", fmt.Errorf("load: %w", &ExitError{Code: ExitLockHeld, Err: base}), ExitLockHeld},
		{"outermost typed wins", &ExitError{Code: ExitAuthFatal, Err: &ExitError{Code: ExitConfig, Err: base}}, ExitAuthFatal},
	}
	for _, tc := range cases {
		if got := ExitCodeOf(tc.err); got != tc.want {
			t.Errorf("%s: ExitCodeOf() = %d, want %d", tc.name, got, tc.want)
		}
	}
	wrapped := &ExitError{Code: ExitConfig, Err: base}
	if !errors.Is(wrapped, base) || wrapped.Error() != "boom" {
		t.Fatalf("ExitError must unwrap to its cause and print its message, got %q", wrapped.Error())
	}
}

// Windows has no POSIX permission bits to assert on.
func runtimeHasUnixModes() bool { return runtime.GOOS != "windows" }
