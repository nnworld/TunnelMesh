package protocol_test

import (
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"testing"
)

func TestTracerouteHopChainDetectsReorderingAndTamper(t *testing.T) {
	chain := protocol.NewTraceHopChain([]byte("trace-key"))
	hop1 := protocol.TraceHop{TraceID: "tr-1", Hop: 1, Role: "client", Result: "forwarded"}
	hop2 := protocol.TraceHop{TraceID: "tr-1", Hop: 2, Role: "server", Result: "forwarded"}
	if err := chain.Append(&hop1); err != nil {
		t.Fatal(err)
	}
	if err := chain.Append(&hop2); err != nil {
		t.Fatal(err)
	}
	if err := protocol.VerifyTraceHopChain([]protocol.TraceHop{hop1, hop2}, []byte("trace-key")); err != nil {
		t.Fatal(err)
	}
	hop2.Role = "evil"
	if err := protocol.VerifyTraceHopChain([]protocol.TraceHop{hop1, hop2}, []byte("trace-key")); err == nil {
		t.Fatal("tampered hop accepted")
	}
	if err := protocol.VerifyTraceHopChain([]protocol.TraceHop{hop2, hop1}, []byte("trace-key")); err == nil {
		t.Fatal("reordered hops accepted")
	}
}

func TestTraceHopRedactionKeepsSensitiveFieldsAdminOnly(t *testing.T) {
	hop := protocol.TraceHop{NodeID: "n1", PrivateIPs: []string{"10.0.0.1"}, RealAddress: "https://internal", Certificate: &protocol.CertificateInfo{Subject: "node"}, Token: &protocol.TokenInfo{ID: "tok"}}
	public := protocol.RedactTraceHop(hop, false)
	if len(public.PrivateIPs) != 0 || public.RealAddress != "" || public.Certificate != nil || public.Token != nil {
		t.Fatalf("sensitive fields leaked: %+v", public)
	}
	admin := protocol.RedactTraceHop(hop, true)
	if len(admin.PrivateIPs) != 1 || admin.RealAddress == "" || admin.Certificate == nil || admin.Token == nil {
		t.Fatalf("admin fields missing: %+v", admin)
	}
}
