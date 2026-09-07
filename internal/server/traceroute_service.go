package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type TracerouteService struct {
	agents   storage.AgentRepository
	metadata *AgentMetadataService
	nodes    storage.NodeRepository
	tokens   storage.ServiceTokenRepository
	key      []byte
	mu       sync.RWMutex
	traces   map[string]protocol.TraceResult
	owners   map[string]string
}

func NewTracerouteService(db *storage.DB) *TracerouteService {
	seed := sha256.Sum256([]byte(fmt.Sprintf("tunnelmesh-trace-%d", time.Now().UnixNano())))
	key := seed[:]
	if configured := strings.TrimSpace(os.Getenv("TUNNELMESH_TRACE_SIGNING_KEY")); configured != "" {
		key = []byte(configured)
	}
	return &TracerouteService{agents: db.Agents(), metadata: NewAgentMetadataService(db.Metadata()), nodes: db.Nodes(), tokens: db.ServiceTokens(), key: key, traces: make(map[string]protocol.TraceResult), owners: make(map[string]string)}
}

func (s *TracerouteService) Start(ctx context.Context, request protocol.TraceRequest, includeSensitive, includeSecrets bool) (protocol.TraceResult, error) {
	return s.StartForUser(ctx, "", request, includeSensitive, includeSecrets)
}

func (s *TracerouteService) StartForUser(ctx context.Context, ownerUserID string, request protocol.TraceRequest, includeSensitive, includeSecrets bool) (protocol.TraceResult, error) {
	if s == nil || s.agents == nil {
		return protocol.TraceResult{}, errors.New("traceroute service unavailable")
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	if request.AgentID == "" {
		return protocol.TraceResult{}, errors.New("agentId is required")
	}
	agent, err := s.agents.Get(ctx, request.AgentID)
	if err != nil {
		return protocol.TraceResult{}, err
	}
	if !agent.Enabled {
		return protocol.TraceResult{}, errors.New("agent is disabled")
	}
	if request.TraceID == "" {
		sum := sha256.Sum256([]byte(request.AgentID + time.Now().UTC().String()))
		request.TraceID = "tr-" + hex.EncodeToString(sum[:8])
	}
	if request.MaxHops <= 0 || request.MaxHops > 16 {
		request.MaxHops = 16
	}
	chain := protocol.NewTraceHopChain(s.key)
	now := time.Now().UTC()
	hops := []protocol.TraceHop{
		{TraceID: request.TraceID, Role: "client", PeerType: "management", Transport: "https", ReceivedAt: now, ForwardedAt: now, Result: "forwarded"},
		{TraceID: request.TraceID, Role: "server", PeerType: "server", Transport: "https", ReceivedAt: now, ForwardedAt: now, Result: "forwarded"},
	}
	if page, listErr := s.nodes.List(ctx, "", 1); listErr == nil && len(page.Items) > 0 {
		node := page.Items[0]
		hops[1].NodeID = node.ID
		if includeSensitive {
			hops[1].RealAddress = node.Address
			var nodeMetadata struct {
				Certificate *protocol.CertificateInfo `json:"certificate"`
			}
			if node.Metadata != "" && json.Unmarshal([]byte(node.Metadata), &nodeMetadata) == nil {
				hops[1].Certificate = nodeMetadata.Certificate
			}
		}
	}
	if s.metadata != nil {
		if metadata, metadataErr := s.metadata.Get(ctx, request.AgentID); metadataErr == nil {
			agentHop := protocol.TraceHop{TraceID: request.TraceID, Role: "agent", NodeID: metadata.NodeID, PeerType: "agent", Transport: "tls-websocket", ReceivedAt: now, ForwardedAt: now, Result: "delivered"}
			if includeSensitive {
				view, viewErr := s.metadata.GetView(ctx, request.AgentID)
				if viewErr == nil {
					for _, item := range view.Items {
						if strings.EqualFold(item.Name, "private_ip") || strings.EqualFold(item.Name, "private_ip_1") {
							if item.Value != "" {
								agentHop.PrivateIPs = append(agentHop.PrivateIPs, item.Value)
							}
						}
					}
				}
			}
			if includeSensitive && s.tokens != nil {
				if tokenPage, tokenErr := s.tokens.List(ctx, storage.ServiceTokenFilter{AgentID: request.AgentID}, "", 1); tokenErr == nil && len(tokenPage.Items) > 0 {
					token := tokenPage.Items[0]
					agentHop.Token = &protocol.TokenInfo{ID: token.ID, Type: string(token.Type), Prefix: token.Prefix, Scope: token.Scope}
				}
			}
			hops = append(hops, agentHop)
		} else {
			hops = append(hops, protocol.TraceHop{TraceID: request.TraceID, Role: "agent", NodeID: agent.ID, PeerType: "agent", Transport: "tls-websocket", ReceivedAt: now, ForwardedAt: now, Result: "delivered"})
		}
	} else {
		hops = append(hops, protocol.TraceHop{TraceID: request.TraceID, Role: "agent", NodeID: agent.ID, Result: "delivered"})
	}
	if len(hops) > request.MaxHops {
		hops = hops[:request.MaxHops]
	}
	for i := range hops {
		hops[i].Hop = i + 1
		if err := chain.Append(&hops[i]); err != nil {
			return protocol.TraceResult{}, err
		}
	}
	for i := range hops {
		hops[i] = protocol.RedactTraceHop(hops[i], includeSensitive)
	}
	result := protocol.TraceResult{TraceID: request.TraceID, Hops: hops, Completed: true}
	s.mu.Lock()
	s.traces[result.TraceID] = result
	s.owners[result.TraceID] = ownerUserID
	s.mu.Unlock()
	return result, nil
}

func (s *TracerouteService) GetForUser(ctx context.Context, traceID, ownerUserID string, admin bool) (protocol.TraceResult, error) {
	result, err := s.Get(ctx, traceID)
	if err != nil {
		return result, err
	}
	s.mu.RLock()
	owner := s.owners[strings.TrimSpace(traceID)]
	s.mu.RUnlock()
	if !admin && owner != "" && owner != ownerUserID {
		return protocol.TraceResult{}, errors.New("trace not found")
	}
	return result, nil
}

func (s *TracerouteService) Get(_ context.Context, traceID string) (protocol.TraceResult, error) {
	s.mu.RLock()
	result, ok := s.traces[strings.TrimSpace(traceID)]
	s.mu.RUnlock()
	if !ok {
		return protocol.TraceResult{}, errors.New("trace not found")
	}
	return result, nil
}
