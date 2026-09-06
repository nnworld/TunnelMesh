package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	maxMetadataFields = 32
	maxMetadataBytes  = 32 << 10
)

type MetadataItem struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Value    string `json:"value,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
}
type AgentMetadataInput struct {
	AgentID    string
	NodeID     string
	Epoch      int64
	Revision   int64
	Items      []MetadataItem
	ReportedAt time.Time
	ExpiresAt  *time.Time
}
type metadataEnvelope struct {
	Items []MetadataItem `json:"items"`
}
type AgentMetadataService struct {
	repo storage.AgentMetadataRepository
}

func NewAgentMetadataService(repo storage.AgentMetadataRepository) *AgentMetadataService {
	return &AgentMetadataService{repo: repo}
}
func (s *AgentMetadataService) Upsert(ctx context.Context, in AgentMetadataInput) (storage.AgentRuntimeMetadata, error) {
	if s == nil || s.repo == nil {
		return storage.AgentRuntimeMetadata{}, errors.New("metadata repository is required")
	}
	if strings.TrimSpace(in.AgentID) == "" || strings.TrimSpace(in.NodeID) == "" || in.Epoch <= 0 || in.Revision <= 0 {
		return storage.AgentRuntimeMetadata{}, errors.New("metadata identity is invalid")
	}
	if len(in.Items) > maxMetadataFields {
		return storage.AgentRuntimeMetadata{}, fmt.Errorf("metadata fields exceed %d", maxMetadataFields)
	}
	items := make([]MetadataItem, len(in.Items))
	for i, item := range in.Items {
		if !metadataNameRE.MatchString(item.Name) {
			return storage.AgentRuntimeMetadata{}, fmt.Errorf("invalid metadata name %q", item.Name)
		}
		if item.Source != "file" && item.Source != "env" {
			return storage.AgentRuntimeMetadata{}, fmt.Errorf("invalid metadata source %q", item.Source)
		}
		if sensitiveMetadataRE.MatchString(item.Name) {
			item.Value = ""
			item.Redacted = true
		}
		items[i] = item
	}
	b, err := json.Marshal(metadataEnvelope{Items: items})
	if err != nil {
		return storage.AgentRuntimeMetadata{}, err
	}
	if len(b) > maxMetadataBytes {
		return storage.AgentRuntimeMetadata{}, fmt.Errorf("metadata payload exceeds %d bytes", maxMetadataBytes)
	}
	now := time.Now().UTC()
	if in.ReportedAt.IsZero() {
		in.ReportedAt = now
	}
	v := storage.AgentRuntimeMetadata{AgentID: in.AgentID, NodeID: in.NodeID, Epoch: in.Epoch, Revision: in.Revision, Metadata: string(b), ReportedAt: in.ReportedAt, LastSeenAt: now, ExpiresAt: in.ExpiresAt, UpdatedAt: now}
	if err := s.repo.Upsert(ctx, v); err != nil {
		return storage.AgentRuntimeMetadata{}, err
	}
	return v, nil
}
func (s *AgentMetadataService) Get(ctx context.Context, agentID string) (storage.AgentRuntimeMetadata, error) {
	v, err := s.repo.Get(ctx, agentID)
	if err == nil && v.ExpiresAt != nil && time.Now().UTC().After(*v.ExpiresAt) {
		v.Stale = true
	}
	return v, err
}

type AgentMetadataView struct {
	storage.AgentRuntimeMetadata
	Items []MetadataItem `json:"items"`
}

func (s *AgentMetadataService) GetView(ctx context.Context, agentID string) (AgentMetadataView, error) {
	v, err := s.Get(ctx, agentID)
	if err != nil {
		return AgentMetadataView{}, err
	}
	var envelope metadataEnvelope
	if err := json.Unmarshal([]byte(v.Metadata), &envelope); err != nil {
		return AgentMetadataView{}, err
	}
	return AgentMetadataView{AgentRuntimeMetadata: v, Items: envelope.Items}, nil
}
func (s *AgentMetadataService) List(ctx context.Context, cursor string, limit int) (storage.Page[storage.AgentRuntimeMetadata], error) {
	return s.repo.List(ctx, cursor, limit)
}
func (s *AgentMetadataService) MarkStale(ctx context.Context, agentID string, epoch int64) error {
	return s.repo.MarkStale(ctx, agentID, epoch)
}

var metadataNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var sensitiveMetadataRE = regexp.MustCompile(`(?i)(password|token|secret|private[_-]?key|dsn)`)
