package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type RemoteValidationRequest struct {
	Protocol   string `json:"protocol"`
	AgentID    string `json:"agentId"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
}

type RemoteValidator struct {
	endpoint string
	client   *http.Client
}

func NewRemoteValidator(endpoint string) *RemoteValidator {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &RemoteValidator{
		endpoint: endpoint,
		client:   &http.Client{Transport: transport, Timeout: 3 * time.Second},
	}
}

func (v *RemoteValidator) Close() {
	if v == nil || v.client == nil {
		return
	}
	v.client.CloseIdleConnections()
}

func (v *RemoteValidator) Validate(ctx context.Context, request RemoteValidationRequest) bool {
	if v == nil || v.endpoint == "" {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	payload, err := json.Marshal(request)
	if err != nil {
		return false
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := v.client.Do(httpRequest)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode >= 200 && response.StatusCode < 300
}
