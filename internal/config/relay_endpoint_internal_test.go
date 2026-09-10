package config

import (
	"net"
	"strings"
	"testing"
)

func TestSelectRelayAdvertiseIPPrefersUsableIPv4(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1").To4()},
		&net.IPNet{IP: net.ParseIP("169.254.1.1").To4()},
		&net.IPNet{IP: net.ParseIP("10.1.2.4").To4()},
		&net.IPNet{IP: net.ParseIP("192.168.10.10").To4()},
		&net.IPNet{IP: net.ParseIP("fd00::10")},
	}
	ip, err := selectRelayAdvertiseIP(addrs)
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "10.1.2.4" {
		t.Fatalf("selectRelayAdvertiseIP() = %s, want 10.1.2.4", ip)
	}
}

func TestSelectRelayAdvertiseIPRejectsNoUsableAddress(t *testing.T) {
	_, err := selectRelayAdvertiseIP([]net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1").To4()},
		&net.IPNet{IP: net.ParseIP("169.254.1.1").To4()},
	})
	if err == nil || !strings.Contains(err.Error(), "no usable relay advertise address") {
		t.Fatalf("selectRelayAdvertiseIP() error = %v, want no usable relay advertise address", err)
	}
}

func TestSelectRelayAdvertiseIPSkipsBroadcastAddress(t *testing.T) {
	_, err := selectRelayAdvertiseIP([]net.Addr{
		&net.IPNet{IP: net.ParseIP("255.255.255.255").To4()},
	})
	if err == nil || !strings.Contains(err.Error(), "no usable relay advertise address") {
		t.Fatalf("selectRelayAdvertiseIP() error = %v, want no usable relay advertise address", err)
	}
}

func TestInferRelayEndpointUsesLocalAddressForWildcardListen(t *testing.T) {
	expectedIP, err := selectRelayAdvertiseIP(localRelayAdvertiseAddresses())
	if err != nil {
		t.Skipf("host has no usable relay advertise address: %v", err)
	}
	want := net.JoinHostPort(expectedIP.String(), "9443")
	for _, listen := range []string{"0.0.0.0:9443", ":9443", "[::]:9443"} {
		endpoint, err := InferRelayEndpoint(listen)
		if err != nil {
			t.Fatalf("InferRelayEndpoint(%q): %v", listen, err)
		}
		if endpoint != want {
			t.Fatalf("InferRelayEndpoint(%q) = %q, want %q", listen, endpoint, want)
		}
	}
}

func TestRelayTLSModeRequiresCompleteMaterial(t *testing.T) {
	useTLS, err := relayTLSMode(RelayConfig{})
	if err != nil || useTLS {
		t.Fatalf("relayTLSMode(empty) = (%v, %v), want (false, nil)", useTLS, err)
	}

	useTLS, err = relayTLSMode(RelayConfig{
		CA:         "/etc/tunnelmesh/certs/ca.pem",
		Cert:       "/etc/tunnelmesh/certs/cert.pem",
		Key:        "/etc/tunnelmesh/certs/key.pem",
		ServerName: "relay.internal.example.com",
	})
	if err != nil || !useTLS {
		t.Fatalf("relayTLSMode(complete) = (%v, %v), want (true, nil)", useTLS, err)
	}

	useTLS, err = relayTLSMode(RelayConfig{CA: "/etc/tunnelmesh/certs/ca.pem"})
	if err == nil || !strings.Contains(err.Error(), "all CA, certificate, and key fields") {
		t.Fatalf("relayTLSMode(partial) = (%v, %v), want complete material error", useTLS, err)
	}

	useTLS, err = relayTLSMode(RelayConfig{
		CA:   "/etc/tunnelmesh/certs/ca.pem",
		Cert: "/etc/tunnelmesh/certs/cert.pem",
		Key:  "/etc/tunnelmesh/certs/key.pem",
	})
	if err == nil || !strings.Contains(err.Error(), "server name") {
		t.Fatalf("relayTLSMode(missing server name) = (%v, %v), want server name error", useTLS, err)
	}
}
