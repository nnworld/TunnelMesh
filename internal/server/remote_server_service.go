package server

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var ErrRemoteServerInvalid = errors.New("invalid remote server")

type RemoteServerInput struct {
	Name            string
	Host            string
	Port            int
	DefaultUsername string
	CredentialID    string
	AgentID         string
	Enabled         bool
}

type RemoteServerPatch struct {
	Name            *string
	Host            *string
	Port            *int
	DefaultUsername *string
	CredentialID    *string
	AgentID         *string
	Enabled         *bool
}

type RemoteServerListFilter = storage.RemoteServerFilter

// RemoteServerDisplay carries joined presentation fields without changing the
// authoritative RemoteServer row. Agent online state is a pointer so unknown
// remains distinct from false for deployments without lease data.
type RemoteServerDisplay struct {
	AgentName      *string
	CredentialName *string
	AgentOnline    *bool
	// CredentialType and CredentialHasSecret tell the admin UI whether SSH and
	// SFTP can authenticate without prompting. Both stay nil when no credential
	// is bound, so "unknown" remains distinct from "cannot auto-authenticate".
	CredentialType      *string
	CredentialHasSecret *bool
}

type RemoteServerService struct {
	servers     storage.RemoteServerRepository
	credentials storage.CredentialRepository
	agents      storage.AgentRepository
	policies    storage.PolicyRepository
	audits      storage.AuditRepository
	leases      storage.LeaseRepository
}

func NewRemoteServerService(servers storage.RemoteServerRepository, credentials storage.CredentialRepository, agents storage.AgentRepository, policies storage.PolicyRepository, audits storage.AuditRepository, leases storage.LeaseRepository) *RemoteServerService {
	return &RemoteServerService{servers: servers, credentials: credentials, agents: agents, policies: policies, audits: audits, leases: leases}
}

func (s *RemoteServerService) Create(ctx context.Context, actor auth.Principal, input RemoteServerInput) (storage.RemoteServer, error) {
	if err := s.validateInput(ctx, actor, input); err != nil {
		return storage.RemoteServer{}, err
	}
	server := storage.RemoteServer{
		OwnerUserID: actor.UserID, Name: strings.TrimSpace(input.Name), Host: input.Host, Port: input.Port,
		DefaultUsername: input.DefaultUsername, CredentialID: input.CredentialID, AgentID: input.AgentID, Enabled: input.Enabled,
	}
	created, err := s.servers.Create(ctx, server)
	if err != nil {
		return storage.RemoteServer{}, err
	}
	s.writeAudit(ctx, actor, "remote_server.created", created.ID)
	return created, nil
}

func (s *RemoteServerService) Get(ctx context.Context, actor auth.Principal, id string) (storage.RemoteServer, error) {
	server, err := s.servers.Get(ctx, id)
	if err != nil {
		return storage.RemoteServer{}, err
	}
	if !isAdmin(actor) && server.OwnerUserID != actor.UserID {
		return storage.RemoteServer{}, ErrResourceForbidden
	}
	return server, nil
}

func (s *RemoteServerService) Update(ctx context.Context, actor auth.Principal, id string, input RemoteServerPatch) (storage.RemoteServer, error) {
	current, err := s.Get(ctx, actor, id)
	if err != nil {
		return storage.RemoteServer{}, err
	}
	next := RemoteServerInput{
		Name: current.Name, Host: current.Host, Port: current.Port, DefaultUsername: current.DefaultUsername,
		CredentialID: current.CredentialID, AgentID: current.AgentID, Enabled: current.Enabled,
	}
	if input.Name != nil {
		next.Name = *input.Name
	}
	if input.Host != nil {
		next.Host = *input.Host
	}
	if input.Port != nil {
		next.Port = *input.Port
	}
	if input.DefaultUsername != nil {
		next.DefaultUsername = *input.DefaultUsername
	}
	if input.CredentialID != nil {
		next.CredentialID = *input.CredentialID
	}
	if input.AgentID != nil {
		next.AgentID = *input.AgentID
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if err := s.validateInput(ctx, actor, next); err != nil {
		return storage.RemoteServer{}, err
	}
	current.Name, current.Host, current.Port = strings.TrimSpace(next.Name), next.Host, next.Port
	current.DefaultUsername, current.CredentialID, current.AgentID, current.Enabled = next.DefaultUsername, next.CredentialID, next.AgentID, next.Enabled
	if err := s.servers.Update(ctx, current); err != nil {
		return storage.RemoteServer{}, err
	}
	updated, err := s.Get(ctx, actor, id)
	if err == nil {
		s.writeAudit(ctx, actor, "remote_server.updated", id)
	}
	return updated, err
}

func (s *RemoteServerService) Delete(ctx context.Context, actor auth.Principal, id string) error {
	if _, err := s.Get(ctx, actor, id); err != nil {
		return err
	}
	if err := s.servers.Delete(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	s.writeAudit(ctx, actor, "remote_server.deleted", id)
	return nil
}

func (s *RemoteServerService) Restore(ctx context.Context, actor auth.Principal, id string) (storage.RemoteServer, error) {
	if _, err := s.Get(ctx, actor, id); err != nil {
		return storage.RemoteServer{}, err
	}
	if err := s.servers.Restore(ctx, id); err != nil {
		return storage.RemoteServer{}, err
	}
	restored, err := s.Get(ctx, actor, id)
	if err == nil {
		s.writeAudit(ctx, actor, "remote_server.restored", id)
	}
	return restored, err
}

func (s *RemoteServerService) List(ctx context.Context, actor auth.Principal, filter RemoteServerListFilter, cursor string, limit int) (storage.Page[storage.RemoteServer], error) {
	if !isAdmin(actor) {
		filter.OwnerUserID = actor.UserID
	}
	return s.servers.List(ctx, filter, cursor, limit)
}

func (s *RemoteServerService) Display(ctx context.Context, server storage.RemoteServer) (RemoteServerDisplay, error) {
	if s == nil {
		return RemoteServerDisplay{}, ErrRemoteServerInvalid
	}
	agent, err := s.agents.Get(ctx, server.AgentID)
	if err != nil {
		return RemoteServerDisplay{}, err
	}
	display := RemoteServerDisplay{AgentName: &agent.Name}
	if server.CredentialID != "" {
		credential, err := s.credentials.Get(ctx, server.CredentialID)
		if err != nil {
			return RemoteServerDisplay{}, err
		}
		display.CredentialName = &credential.Name
		credentialType := string(credential.Type)
		hasSecret := credential.HasSecret()
		display.CredentialType = &credentialType
		display.CredentialHasSecret = &hasSecret
	}
	if s.leases != nil {
		leases, err := s.leases.ListActiveByAgent(ctx, server.AgentID)
		if err != nil {
			return RemoteServerDisplay{}, err
		}
		online := len(leases) > 0
		display.AgentOnline = &online
	}
	return display, nil
}

func (s *RemoteServerService) validateInput(ctx context.Context, actor auth.Principal, input RemoteServerInput) error {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Host) == "" || strings.TrimSpace(input.DefaultUsername) == "" {
		return errors.New("remote server name, host and username are required")
	}
	if input.Port < 1 || input.Port > 65535 {
		return ErrRemoteServerInvalid
	}
	if input.AgentID == "" {
		return errors.New("remote server agent is required")
	}
	agent, err := s.agents.Get(ctx, input.AgentID)
	if err != nil {
		return err
	}
	if !agent.Enabled {
		return errors.New("remote server agent is unavailable")
	}
	if !isAdmin(actor) && agent.OwnerUserID != actor.UserID {
		return ErrResourceForbidden
	}
	if input.CredentialID != "" {
		credential, err := s.credentials.Get(ctx, input.CredentialID)
		if err != nil {
			return err
		}
		if credential.DeletedAt != nil || !credential.Enabled || credential.OwnerUserID != actor.UserID {
			return errors.New("remote server credential is unavailable")
		}
	}
	return s.validateAgentPolicy(ctx, input.AgentID, input.Host, input.Port)
}

func (s *RemoteServerService) validateAgentPolicy(ctx context.Context, agentID, host string, port int) error {
	ip := net.ParseIP(host)
	if ip == nil {
		// DNS names are resolved by the Agent and cannot be safely matched to
		// a CIDR allowlist here. Reject them until policy records support a
		// validated domain allowlist.
		return errors.New("remote server host must be an IPv4 address")
	}
	policies, err := s.policies.ListByAgent(ctx, agentID, "", 500)
	if err != nil {
		return err
	}
	allowed := false
	var lastErr error
	for _, policy := range policies.Items {
		parsed, err := routing.NewPolicy(nonemptySplit(policy.AllowedCIDRs), nonemptySplit(policy.AllowedPorts))
		if err != nil {
			continue
		}
		policyErr := parsed.Validate(ip, port)
		if policyErr == nil {
			allowed = true
			break
		}
		lastErr = policyErr
	}
	if !allowed {
		if lastErr == nil {
			lastErr = errors.New("no matching agent policy")
		}
		return lastErr
	}
	return nil
}

func (s *RemoteServerService) writeAudit(ctx context.Context, actor auth.Principal, action, id string) {
	if s == nil || s.audits == nil {
		return
	}
	_ = s.audits.Create(ctx, storage.AuditLog{ActorUserID: actor.UserID, Action: action, ResourceType: "remote_server", ResourceID: id, Details: "{}"})
}

func parsePortInt(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return port, nil
}
