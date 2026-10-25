package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func helperPreflight(version, checkExit string) ExecPreflight {
	return ExecPreflight{
		Args: []string{"--config", "/etc/tunnelmesh/client.yaml", "run"},
		Env: append(os.Environ(), "SELFUPDATE_HELPER=1", "SELFUPDATE_HELPER_VERSION="+version,
			"SELFUPDATE_HELPER_CHECK_EXIT="+checkExit, "TUNNELMESH_WORKER=1"),
		Timeout: 20 * time.Second,
	}
}

func TestPreflightAcceptsABinaryThatReportsTheTargetAndAcceptsTheConfig(t *testing.T) {
	if err := helperPreflight("v1.4.0", "0").Check(context.Background(), os.Args[0], "v1.4.0"); err != nil {
		t.Fatalf("Check() = %v, want success", err)
	}
}

// A release whose binary says it is another version was mispackaged, and running
// it would make every later decision wrong.
func TestPreflightRejectsABinaryThatReportsAnotherVersion(t *testing.T) {
	err := helperPreflight("v1.3.9", "0").Check(context.Background(), os.Args[0], "v1.4.0")
	if err == nil || !strings.Contains(err.Error(), "v1.3.9") {
		t.Fatalf("Check() = %v, want a version mismatch naming what it reported", err)
	}
}

// The typical upgrade failure: the new version no longer accepts the existing
// configuration. The message must reach the log so an operator can see why.
func TestPreflightRejectsAConfigTheNewVersionDoesNotAccept(t *testing.T) {
	err := helperPreflight("v1.4.0", "78").Check(context.Background(), os.Args[0], "v1.4.0")
	if err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("Check() = %v, want the new binary's own explanation", err)
	}
}

func TestPreflightReportsABinaryThatCannotBeExecuted(t *testing.T) {
	p := helperPreflight("v1.4.0", "0")
	if err := p.Check(context.Background(), filepath.Join(t.TempDir(), "missing"), "v1.4.0"); err == nil {
		t.Fatal("Check() succeeded for a binary that does not exist")
	}
	notExec := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(notExec, []byte("not a program"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(context.Background(), notExec, "v1.4.0"); err == nil {
		t.Fatal("Check() succeeded for a file that is not executable")
	}
}

func TestPreflightIsBoundedByItsTimeout(t *testing.T) {
	p := helperPreflight("v1.4.0", "0")
	p.Timeout = time.Nanosecond
	if err := p.Check(context.Background(), os.Args[0], "v1.4.0"); err == nil {
		t.Fatal("Check() ignored its timeout")
	}
}

func TestCheckConfigArgs(t *testing.T) {
	cases := []struct {
		in      []string
		want    string
		wantErr bool
	}{
		{[]string{"--config", "/x.yaml", "run"}, "--config /x.yaml check-config", false},
		{[]string{"run", "--config", "/x.yaml"}, "check-config --config /x.yaml", false},
		{[]string{"--config=/x.yaml", "run"}, "--config=/x.yaml check-config", false},
		{[]string{"--config", "/x.yaml"}, "", true},
		{nil, "", true},
	}
	for _, tc := range cases {
		got, err := checkConfigArgs(tc.in)
		if (err != nil) != tc.wantErr || strings.Join(got, " ") != tc.want {
			t.Errorf("checkConfigArgs(%v) = %v, %v; want %q (error=%v)", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
