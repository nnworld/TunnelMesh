package cli

import (
	"fmt"
	"os"
	"testing"
)

// e2eBadReleaseEnv makes this test binary impersonate a broken release for the
// self-upgrade end-to-end test: it reports the published version and accepts the
// configuration, then dies the moment it is started as a worker.
const e2eBadReleaseEnv = "CLI_E2E_BAD_RELEASE"

// The tests in this package exercise `run` in-process. Self-upgrade and
// supervision are on by default in real use, and with them on `run` would start a
// supervisor that launches this very test binary, so they are switched off for the
// package. The tests that cover supervision switch them back on explicitly.
func TestMain(m *testing.M) {
	if os.Getenv(e2eBadReleaseEnv) == "1" {
		runBadRelease()
		return
	}
	for _, role := range []string{"CLIENT", "AGENT"} {
		_ = os.Setenv("TUNNELMESH_"+role+"_UPDATE_ENABLED", "false")
		_ = os.Setenv("TUNNELMESH_"+role+"_UPDATE_SUPERVISE", "false")
	}
	code := m.Run()
	removeBuiltBinaries()
	os.Exit(code)
}

func runBadRelease() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version":
			fmt.Println("tunnelmesh-agent v1.4.0 commit=e2e built=e2e")
			os.Exit(0)
		case "check-config":
			os.Exit(0)
		}
	}
	// Anything else is the worker run: crash straight away.
	os.Exit(1)
}
