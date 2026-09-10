package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemoteValidatorAllowsOn2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	validator := NewRemoteValidator(server.URL)
	allowed := validator.Validate(context.Background(), RemoteValidationRequest{
		Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443,
	})
	if !allowed {
		t.Fatal("remote validator denied a 2xx response")
	}
}

func TestRemoteValidatorDeniesOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	validator := NewRemoteValidator(server.URL)
	allowed := validator.Validate(context.Background(), RemoteValidationRequest{
		Protocol: "http-proxy", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 80,
	})
	if allowed {
		t.Fatal("remote validator allowed a non-2xx response")
	}
}

func TestRemoteValidatorDeniesOnNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.Close()

	validator := NewRemoteValidator(server.URL)
	allowed := validator.Validate(context.Background(), RemoteValidationRequest{
		Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443,
	})
	if allowed {
		t.Fatal("remote validator allowed a network error")
	}
}

func TestRemoteValidatorSendsContext(t *testing.T) {
	var got RemoteValidationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s, want POST", r.Method)
		}
		if gotHeader := r.Header.Get("Content-Type"); gotHeader != "application/json" {
			t.Fatalf("Content-Type=%q", gotHeader)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	validator := NewRemoteValidator(server.URL)
	request := RemoteValidationRequest{
		Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443,
		Username: "alice", Password: "secret",
	}
	if !validator.Validate(context.Background(), request) {
		t.Fatal("remote validator denied a valid request")
	}
	if got != request {
		t.Fatalf("remote request=%+v, want %+v", got, request)
	}
}

func TestRemoteValidatorUsesBoundedTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	validator := NewRemoteValidator(server.URL)
	start := time.Now()
	if !validator.Validate(context.Background(), RemoteValidationRequest{Protocol: "socks5"}) {
		t.Fatal("remote validator denied a fast 2xx response")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("remote validation took %s, want bounded timeout", elapsed)
	}
}

func TestRemoteValidatorDrainsResponseBodyBeforeClose(t *testing.T) {
	var drained bool
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body := &trackingBody{data: []byte(`{"allowed":true}`)}
		body.onEOF = func() { drained = true }
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})

	validator := &RemoteValidator{
		endpoint: "http://auth.internal/validate",
		client:   &http.Client{Transport: transport},
	}
	if !validator.Validate(context.Background(), RemoteValidationRequest{Protocol: "socks5"}) {
		t.Fatal("remote validator denied a 2xx response")
	}
	if !drained {
		t.Fatal("response body was closed without being drained to EOF")
	}
}

func TestRemoteValidatorCloseClosesIdleConnections(t *testing.T) {
	transport := &closeIdleTransport{roundTrip: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
	})}
	validator := &RemoteValidator{
		endpoint: "http://auth.internal/validate",
		client:   &http.Client{Transport: transport},
	}

	validator.Close()

	if !transport.closedIdle {
		t.Fatal("RemoteValidator.Close did not close idle connections")
	}
}

type closeIdleTransport struct {
	roundTrip  roundTripperFunc
	closedIdle bool
}

func (t *closeIdleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.roundTrip(r)
}

func (t *closeIdleTransport) CloseIdleConnections() {
	t.closedIdle = true
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type trackingBody struct {
	data   []byte
	onEOF  func()
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		if b.onEOF != nil {
			b.onEOF()
		}
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}
