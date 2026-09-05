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
