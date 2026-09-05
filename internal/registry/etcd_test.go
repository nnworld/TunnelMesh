package registry

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestEtcdRegistryKeyFormat(t *testing.T) {
	r := NewEtcdRegistry(nil, "")
	if got, want := r.nodeKey("n1"), "/tunnelmesh/nodes/n1"; got != want {
		t.Fatalf("node key = %q, want %q", got, want)
	}
	if got, want := r.agentKey("a1"), "/tunnelmesh/agents/a1"; got != want {
		t.Fatalf("agent key = %q, want %q", got, want)
	}
}

func TestEtcdRegistryContract(t *testing.T) {
	endpoints := strings.Split(strings.TrimSpace(os.Getenv("TUNNELMESH_TEST_ETCD_ENDPOINTS")), ",")
	if len(endpoints) == 0 || endpoints[0] == "" {
		t.Skip("TUNNELMESH_TEST_ETCD_ENDPOINTS is not set")
	}
	r, err := NewEtcdRegistryFromEndpoints(context.Background(), endpoints, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	runRegistryContract(t, func(*testing.T) NodeRegistry { return r })
}
