package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// nativeTLSListener applies optional process-native TLS termination. The
// disabled case deliberately returns the original listener for internal HTTP
// deployments behind Nginx.
func nativeTLSListener(listener net.Listener, cfg config.TLSConfig) (net.Listener, error) {
	if listener == nil {
		return nil, fmt.Errorf("native TLS listener is required")
	}
	if !cfg.Enabled {
		return listener, nil
	}
	if strings.TrimSpace(cfg.CertFile) == "" || strings.TrimSpace(cfg.KeyFile) == "" {
		return nil, fmt.Errorf("native TLS requires certificate and private key files")
	}
	var minVersion uint16
	switch cfg.MinVersion {
	case "1.2":
		minVersion = tls.VersionTLS12
	case "1.3":
		minVersion = tls.VersionTLS13
	default:
		return nil, fmt.Errorf("native TLS minimum version must be 1.2 or 1.3")
	}
	pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load native TLS certificate: %w", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   minVersion,
	}
	return tls.NewListener(listener, tlsConfig), nil
}
