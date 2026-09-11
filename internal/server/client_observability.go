package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var clientMetadataNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var clientMetadataSensitivePattern = regexp.MustCompile(`(?i)(password|passphrase|token|secret|private[_-]?key|api[_-]?key|credential|authorization|cookie|dsn)`)

const DefaultClientMetadataTTL = 5 * time.Minute

// ErrClientMetadataHelloRequired enforces the protocol ordering: metadata
// updates are valid only after an accepted CLIENT_HELLO for that connection.
var ErrClientMetadataHelloRequired = errors.New("client metadata hello is required")

// maxClientMetadataFields mirrors the collector limit at the trust boundary;
// a compromised client must not bypass the configured metadata cardinality.
const maxClientMetadataFields = 32

// ClientObservabilityService owns the durable side of Client observability.
// It never participates in authorization and never stores token plaintext.
type ClientObservabilityService struct {
	instances    storage.ClientInstanceRepository
	leases       *ClientConnectionLeaseController
	manager      *ClientSessionManager
	serverNodeID string
	metadataTTL  time.Duration

	mu          sync.Mutex
	revisions   map[string]uint64
	instanceIDs map[string]string
}

func NewClientObservabilityService(instances storage.ClientInstanceRepository, leases *ClientConnectionLeaseController, manager *ClientSessionManager, serverNodeID string, metadataTTL time.Duration) *ClientObservabilityService {
	if metadataTTL <= 0 {
		metadataTTL = DefaultClientMetadataTTL
	}
	return &ClientObservabilityService{
		instances: instances, leases: leases, manager: manager,
		serverNodeID: serverNodeID, metadataTTL: metadataTTL,
		revisions: make(map[string]uint64), instanceIDs: make(map[string]string),
	}
}

func (s *ClientObservabilityService) Hello(ctx context.Context, principal ClientSessionPrincipal, payload protocol.ClientMetadataPayload) (protocol.ClientMetadataAckPayload, error) {
	instance, ack, err := s.persistMetadata(ctx, principal, payload)
	if err != nil {
		return ack, err
	}
	record, ok := s.manager.Get(principal.ConnectionID)
	if !ok {
		return rejectedClientMetadataAck(payload, "session_closed"), ErrSessionClosed
	}
	record.ClientInstanceID = instance.ID
	record.MetadataEnabled = true
	if _, err := s.leases.Register(ctx, record); err != nil {
		return rejectedClientMetadataAck(payload, "lease_register_failed"), err
	}
	s.manager.SetClientInstanceID(principal.ConnectionID, instance.ID)
	s.rememberRevision(principal.ConnectionID, payload.Revision)
	s.rememberInstanceID(principal.ConnectionID, payload.InstanceID)
	return protocol.ClientMetadataAckPayload{
		ClientInstanceID: instance.ID, InstanceID: payload.InstanceID,
		ConnectionID: principal.ConnectionID, Revision: payload.Revision, Accepted: true,
	}, nil
}

func (s *ClientObservabilityService) Update(ctx context.Context, principal ClientSessionPrincipal, payload protocol.ClientMetadataPayload) (protocol.ClientMetadataAckPayload, error) {
	if s.currentRevision(principal.ConnectionID) == 0 {
		return rejectedClientMetadataAck(payload, "client_hello_required"), ErrClientMetadataHelloRequired
	}
	if current := s.currentRevision(principal.ConnectionID); current != 0 && payload.Revision <= current {
		record, ok := s.manager.Get(principal.ConnectionID)
		if !ok {
			return rejectedClientMetadataAck(payload, "session_closed"), ErrSessionClosed
		}
		return protocol.ClientMetadataAckPayload{
			ClientInstanceID: record.ClientInstanceID, InstanceID: payload.InstanceID,
			ConnectionID: principal.ConnectionID, Revision: current, Accepted: true, Idempotent: true,
		}, nil
	}
	instance, ack, err := s.persistMetadata(ctx, principal, payload)
	if err != nil {
		return ack, err
	}
	s.manager.SetClientInstanceID(principal.ConnectionID, instance.ID)
	s.rememberRevision(principal.ConnectionID, payload.Revision)
	s.rememberInstanceID(principal.ConnectionID, payload.InstanceID)
	return protocol.ClientMetadataAckPayload{
		ClientInstanceID: instance.ID, InstanceID: payload.InstanceID,
		ConnectionID: principal.ConnectionID, Revision: payload.Revision, Accepted: true,
	}, nil
}

func (s *ClientObservabilityService) RegisterLegacy(ctx context.Context, principal ClientSessionPrincipal) (string, error) {
	if err := validateClientObservabilityPrincipal(principal); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	instance := storage.ClientInstance{
		OwnerUserID: principal.Identity.OwnerUserID,
		InstanceID:  "legacy-" + principal.ConnectionID,
		Metadata:    "{}", Capabilities: "",
		ReportedAt: now, LastSeenAt: now, ExpiresAt: ptrTime(now.Add(s.metadataTTL)), UpdatedAt: now,
	}
	persisted, err := s.instances.Upsert(ctx, instance)
	if err != nil {
		return "", err
	}
	record, ok := s.manager.Get(principal.ConnectionID)
	if !ok {
		return "", ErrSessionClosed
	}
	record.ClientInstanceID = persisted.ID
	if _, err := s.leases.Register(ctx, record); err != nil {
		return "", err
	}
	s.manager.SetClientInstanceID(principal.ConnectionID, persisted.ID)
	s.rememberInstanceID(principal.ConnectionID, instance.InstanceID)
	return persisted.ID, nil
}

func (s *ClientObservabilityService) Heartbeat(ctx context.Context, principal ClientSessionPrincipal) error {
	record, ok := s.manager.Get(principal.ConnectionID)
	if !ok {
		return ErrSessionClosed
	}
	if err := s.leases.Heartbeat(ctx, record); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.instances.TouchInstance(ctx, record.OwnerUserID, s.currentInstanceID(record.ConnectionID), now, now.Add(s.metadataTTL))
}

func (s *ClientObservabilityService) StreamOpened(principal ClientSessionPrincipal) error {
	if _, ok := s.manager.Get(principal.ConnectionID); !ok {
		return ErrSessionClosed
	}
	s.manager.ObserveStreamOpened(principal.ConnectionID)
	return nil
}

func (s *ClientObservabilityService) StreamClosed(principal ClientSessionPrincipal) error {
	if _, ok := s.manager.Get(principal.ConnectionID); !ok {
		return ErrSessionClosed
	}
	s.manager.ObserveStreamClosed(principal.ConnectionID)
	return nil
}

func (s *ClientObservabilityService) Release(ctx context.Context, principal ClientSessionPrincipal) error {
	record, ok := s.manager.Get(principal.ConnectionID)
	if !ok {
		return ErrSessionClosed
	}
	err := s.leases.Release(ctx, record.ConnectionID, record.ConnectionEpoch)
	s.manager.Remove(record.ConnectionID)
	s.forgetConnection(record.ConnectionID)
	return err
}

func (s *ClientObservabilityService) persistMetadata(ctx context.Context, principal ClientSessionPrincipal, payload protocol.ClientMetadataPayload) (storage.ClientInstance, protocol.ClientMetadataAckPayload, error) {
	if err := validateClientObservabilityPrincipal(principal); err != nil {
		return storage.ClientInstance{}, rejectedClientMetadataAck(payload, "invalid_principal"), err
	}
	if err := validateClientMetadataPayload(payload); err != nil {
		return storage.ClientInstance{}, rejectedClientMetadataAck(payload, "invalid_payload"), err
	}
	metadata, err := json.Marshal(payload)
	if err != nil {
		return storage.ClientInstance{}, rejectedClientMetadataAck(payload, "invalid_payload"), err
	}
	capabilities, err := json.Marshal(payload.Capabilities)
	if err != nil {
		return storage.ClientInstance{}, rejectedClientMetadataAck(payload, "invalid_payload"), err
	}
	now := time.Now().UTC()
	instance := storage.ClientInstance{
		OwnerUserID: principal.Identity.OwnerUserID, InstanceID: payload.InstanceID,
		Metadata: string(metadata), Capabilities: string(capabilities),
		ReportedAt: payload.ReportedAt, LastSeenAt: now,
		ExpiresAt: ptrTime(now.Add(s.metadataTTL)), UpdatedAt: now,
	}
	persisted, err := s.instances.Upsert(ctx, instance)
	if err != nil {
		return storage.ClientInstance{}, rejectedClientMetadataAck(payload, "persist_failed"), err
	}
	return persisted, protocol.ClientMetadataAckPayload{}, nil
}

func validateClientObservabilityPrincipal(principal ClientSessionPrincipal) error {
	if principal.ConnectionID == "" || principal.Identity.TokenID == "" || principal.Identity.OwnerUserID == "" {
		return errors.New("client observability principal is incomplete")
	}
	if principal.Identity.Type != storage.TokenTypeClient {
		return errors.New("client metadata requires a client token")
	}
	return nil
}

func validateClientMetadataPayload(payload protocol.ClientMetadataPayload) error {
	if strings.TrimSpace(payload.InstanceID) == "" || payload.Revision == 0 || payload.ReportedAt.IsZero() {
		return errors.New("client metadata identity is invalid")
	}
	if len(payload.Items) > maxClientMetadataFields {
		return fmt.Errorf("client metadata field count exceeds %d", maxClientMetadataFields)
	}
	seen := make(map[string]struct{}, len(payload.Items))
	for _, item := range payload.Items {
		if item.Name == "" || len(item.Name) > 64 || !clientMetadataNamePattern.MatchString(item.Name) {
			return fmt.Errorf("invalid client metadata name %q", item.Name)
		}
		if _, exists := seen[item.Name]; exists {
			return fmt.Errorf("duplicate client metadata name %q", item.Name)
		}
		seen[item.Name] = struct{}{}
		if clientMetadataSensitivePattern.MatchString(item.Name) {
			return fmt.Errorf("sensitive client metadata name %q", item.Name)
		}
		if len(item.Value) > 4096 || !utf8.ValidString(item.Value) {
			return fmt.Errorf("invalid client metadata value for %q", item.Name)
		}
	}
	return nil
}

func rejectedClientMetadataAck(payload protocol.ClientMetadataPayload, code string) protocol.ClientMetadataAckPayload {
	return protocol.ClientMetadataAckPayload{
		InstanceID: payload.InstanceID, Revision: payload.Revision, Accepted: false,
		Errors: []protocol.ClientMetadataError{{Name: "client_metadata", Code: code, Message: "client metadata was rejected"}},
	}
}

func (s *ClientObservabilityService) rememberRevision(connectionID string, revision uint64) {
	s.mu.Lock()
	s.revisions[connectionID] = revision
	s.mu.Unlock()
}

func (s *ClientObservabilityService) currentRevision(connectionID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revisions[connectionID]
}

func (s *ClientObservabilityService) rememberInstanceID(connectionID, instanceID string) {
	s.mu.Lock()
	s.instanceIDs[connectionID] = instanceID
	s.mu.Unlock()
}

func (s *ClientObservabilityService) forgetConnection(connectionID string) {
	s.mu.Lock()
	delete(s.revisions, connectionID)
	delete(s.instanceIDs, connectionID)
	s.mu.Unlock()
}

func (s *ClientObservabilityService) currentInstanceID(connectionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if instanceID := s.instanceIDs[connectionID]; instanceID != "" {
		return instanceID
	}
	return "legacy-" + connectionID
}

func ptrTime(value time.Time) *time.Time { return &value }
