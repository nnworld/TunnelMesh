package selfupdate

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestMain lets the test binary stand in for a downloaded release binary, so the
// preflight can be tested against a real process.
func TestMain(m *testing.M) {
	if os.Getenv("SELFUPDATE_HELPER") == "1" {
		runFakeRelease()
		return
	}
	os.Exit(m.Run())
}

func runFakeRelease() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version":
			fmt.Printf("tunnelmesh-client %s commit=abc built=today\n", os.Getenv("SELFUPDATE_HELPER_VERSION"))
			os.Exit(0)
		case "check-config":
			if os.Getenv("TUNNELMESH_WORKER") != "" {
				fmt.Println("worker variable leaked into the preflight")
				os.Exit(99)
			}
			code, _ := strconv.Atoi(os.Getenv("SELFUPDATE_HELPER_CHECK_EXIT"))
			if code != 0 {
				fmt.Println("configuration invalid: unknown mode")
			} else {
				fmt.Println("configuration valid")
			}
			os.Exit(code)
		}
	}
	os.Exit(98)
}
