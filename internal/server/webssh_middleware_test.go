package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestWebSSHOriginValidation(t *testing.T) {
	security := config.SecurityConfig{AllowedOrigins: []string{"https://admin.example.com"}}
	cases := []struct {
		name   string
		origin string
		valid  bool
	}{
		{name: "allowed", origin: "https://admin.example.com", valid: true},
		{name: "missing", origin: "", valid: false},
		{name: "malformed", origin: "https://user@admin.example.com/path", valid: false},
		{name: "not listed", origin: "https://evil.example.com", valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/ws/webssh/id", nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			err := validateWebSSHOrigin(security, request)
			if tc.valid && err != nil {
				t.Fatalf("valid origin rejected: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("invalid origin accepted")
			}
		})
	}
}
