package registry

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestEtcdConcurrentTakeoverUsesUniqueEpoch(t *testing.T) {
	endpoints := strings.Split(strings.TrimSpace(os.Getenv("TUNNELMESH_TEST_ETCD_ENDPOINTS")), ",")
	if len(endpoints) == 0 || endpoints[0] == "" {
		t.Skip("TUNNELMESH_TEST_ETCD_ENDPOINTS is not set")
	}
	r, err := NewEtcdRegistryFromEndpoints(context.Background(), endpoints, "/tunnelmesh-test-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	first, err := r.Register(ctx, NodeRegistration{NodeID: "n0", AgentID: "a-concurrent", TTL: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	var wg sync.WaitGroup
	owners := make(chan NodeOwner, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner, e := r.Register(ctx, NodeRegistration{NodeID: "n" + string(rune('1'+i)), AgentID: "a-concurrent", TTL: time.Second})
			if e == nil {
				owners <- owner
			}
		}(i)
	}
	wg.Wait()
	close(owners)
	var got []NodeOwner
	for owner := range owners {
		got = append(got, owner)
	}
	if len(got) != 1 {
		t.Fatalf("successful takeover owners=%d, want 1", len(got))
	}
	if got[0].Epoch != first.Epoch+1 {
		t.Fatalf("takeover epoch=%d, want %d", got[0].Epoch, first.Epoch+1)
	}
}
