package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// nativeTLSConfig builds the process-native TLS configuration, or returns a nil
// configuration when native termination is disabled. The validation rules live
// here so the startup preflight and the listener wrap cannot drift apart.
func nativeTLSConfig(cfg config.TLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
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
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   minVersion,
	}, nil
}

// PreflightNativeTLS validates the native TLS material without binding a
// listener. Startup calls it before opening storage because reading a certificate
// is cheap while opening the database can run a full schema migration; without the
// preflight a missing certificate surfaces as a storage timeout once the migration
// outgrows the caller's deadline.
func PreflightNativeTLS(cfg config.TLSConfig) error {
	_, err := nativeTLSConfig(cfg)
	return err
}

// nativeTLSListener applies optional process-native TLS termination. The
// disabled case deliberately returns the original listener for internal HTTP
// deployments behind Nginx.
func nativeTLSListener(listener net.Listener, cfg config.TLSConfig) (net.Listener, error) {
	if listener == nil {
		return nil, fmt.Errorf("native TLS listener is required")
	}
	tlsConfig, err := nativeTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	if tlsConfig == nil {
		return listener, nil
	}
	return tls.NewListener(listener, tlsConfig), nil
}
