package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DiagnoseRequest is intentionally small: the target response is never
// returned or persisted, only a bounded result classification is exposed.
type DiagnoseRequest struct {
	Kind    string        `json:"kind"`
	Host    string        `json:"host"`
	Port    int           `json:"port"`
	Timeout time.Duration `json:"timeout"`
}

type ProbeResult struct {
	ProbeID    string        `json:"probeId"`
	AgentID    string        `json:"agentId"`
	Kind       string        `json:"kind"`
	Result     string        `json:"result"`
	ErrorClass string        `json:"errorClass,omitempty"`
	Duration   time.Duration `json:"duration"`
	ObservedAt time.Time     `json:"observedAt"`
}

type ProbeExecutor interface {
	Execute(context.Context, DiagnoseRequest) ProbeResult
}

type ProbeService struct {
	agents storage.AgentRepository
	probes storage.AgentProbeResultRepository
	leases storage.LeaseRepository
	exec   ProbeExecutor
}

func NewProbeService(source any, executors ...ProbeExecutor) *ProbeService {
	s := &ProbeService{}
	switch v := source.(type) {
	case *storage.DB:
		s.agents, s.probes, s.leases = v.Agents(), v.ProbeResults(), v.Leases()
	case storage.AgentRepository:
		s.agents = v
	}
	if len(executors) > 0 {
		s.exec = executors[0]
	}
	return s
}

func (s *ProbeService) Diagnose(ctx context.Context, principal auth.Principal, agentID string, req DiagnoseRequest) (ProbeResult, error) {
	if s == nil || s.agents == nil || s.probes == nil || s.leases == nil {
		return ProbeResult{}, errors.New("probe service unavailable")
	}
	agent, err := s.agents.Get(ctx, strings.TrimSpace(agentID))
	if err != nil {
		return ProbeResult{}, err
	}
	if principal.Role != "admin" && principal.UserID != agent.OwnerUserID {
		return ProbeResult{}, auth.ErrForbidden
	}
	if err := validateDiagnoseRequest(req); err != nil {
		return ProbeResult{}, err
	}
	lease, err := s.leases.Get(ctx, agent.ID)
	if err != nil {
		return ProbeResult{}, err
	}
	probeCtx := ctx
	if req.Timeout == 0 {
		req.Timeout = 10 * time.Second
	}
	var cancel context.CancelFunc
	probeCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	result := ProbeResult{ProbeID: newProbeID(), AgentID: agent.ID, Kind: strings.ToLower(strings.TrimSpace(req.Kind)), Result: "failure", ErrorClass: "unsupported", ObservedAt: time.Now().UTC()}
	if s.exec != nil {
		result = s.exec.Execute(probeCtx, req)
		result.AgentID = agent.ID
		if result.ProbeID == "" {
			result.ProbeID = newProbeID()
		}
		if result.ObservedAt.IsZero() {
			result.ObservedAt = time.Now().UTC()
		}
		result.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
		if result.ErrorClass == "" && result.Result == "success" {
			result.ErrorClass = "none"
		}
	}
	if !boundedProbeClass(result.ErrorClass) {
		result.ErrorClass = "internal"
	}
	if err := s.probes.Create(ctx, storage.AgentProbeResult{ProbeID: result.ProbeID, AgentID: result.AgentID, NodeID: lease.NodeID, Epoch: lease.Epoch, Kind: result.Kind, Result: result.Result, ErrorClass: result.ErrorClass, Duration: result.Duration, ObservedAt: result.ObservedAt}); err != nil {
		return ProbeResult{}, err
	}
	return result, nil
}

func validateDiagnoseRequest(req DiagnoseRequest) error {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "tcp" && kind != "http" && kind != "udp" {
		return errors.New("unsupported probe kind")
	}
	if strings.TrimSpace(req.Host) == "" || len(req.Host) > 255 || req.Port < 1 || req.Port > 65535 {
		return errors.New("invalid probe target")
	}
	if req.Timeout != 0 && (req.Timeout < 10*time.Millisecond || req.Timeout > 30*time.Second) {
		return errors.New("invalid probe timeout")
	}
	return nil
}

func boundedProbeClass(class string) bool {
	switch class {
	case "", "none", "timeout", "dns_timeout", "connect_timeout", "read_timeout", "connection_refused", "policy_denied", "epoch_stale", "unsupported", "cancelled", "internal", "http_status", "invalid_response":
		return true
	default:
		return false
	}
}

func newProbeID() string { return "probe-" + time.Now().UTC().Format("20060102150405.000000000") }
