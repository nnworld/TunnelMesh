package build_test

import (
	"reflect"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
)

func TestBinaryNamesExposeAllCommands(t *testing.T) {
	want := []string{
		"tunnelmesh-server",
		"tunnelmesh-agent",
		"tunnelmesh-client",
	}

	if got := build.BinaryNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("BinaryNames() = %v, want %v", got, want)
	}
}

func TestCurrentReturnsLinkerMetadata(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := build.Version, build.Commit, build.BuildTime
	t.Cleanup(func() {
		build.Version, build.Commit, build.BuildTime = originalVersion, originalCommit, originalBuildTime
	})
	build.Version = "v1.2.3"
	build.Commit = "0123456789abcdef"
	build.BuildTime = "2026-09-11T10:00:00Z"

	want := build.Info{Version: "v1.2.3", Commit: "0123456789abcdef", BuildTime: "2026-09-11T10:00:00Z"}
	if got := build.Current(); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestStringIsStableAndSingleLine(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := build.Version, build.Commit, build.BuildTime
	t.Cleanup(func() {
		build.Version, build.Commit, build.BuildTime = originalVersion, originalCommit, originalBuildTime
	})
	build.Version = "v1.2.3"
	build.Commit = "0123456789abcdef"
	build.BuildTime = "2026-09-11T10:00:00Z"

	if got, want := build.String(), "v1.2.3 commit=0123456789abcdef built=2026-09-11T10:00:00Z"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
