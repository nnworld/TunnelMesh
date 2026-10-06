package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"gopkg.in/yaml.v3"
)

// ClientSettings is the subset of client.yaml the tray owns.
//
// Everything else in the file - storage, connection-pool tuning, stream buffers,
// operator comments - belongs to whoever wrote it and is carried through untouched.
// The tray is one editor of a shared file, not its schema.
type ClientSettings struct {
	Mode      string
	ServerURL string
	Token     string
	Tunnels   []config.TunnelConfig
}

// ConfigStore reads and writes client.yaml.
type ConfigStore struct{ path string }

// NewConfigStore binds a store to a client.yaml path.
func NewConfigStore(path string) *ConfigStore { return &ConfigStore{path: path} }

// Path returns the backing file.
func (s *ConfigStore) Path() string { return s.path }

// Exists reports whether the configuration file is present.
func (s *ConfigStore) Exists() bool {
	info, err := os.Stat(s.path)
	return err == nil && !info.IsDir()
}

// tunnelYAML mirrors config.TunnelConfig with omitempty.
//
// The shared struct carries no omitempty tags, so encoding it directly would give a
// socks5 tunnel empty target_host and target_port keys and imply a target that does
// not exist. Retagging the shared struct is not an option: it is the CLI's
// serialization contract.
type tunnelYAML struct {
	Name        string `yaml:"name,omitempty"`
	Protocol    string `yaml:"protocol,omitempty"`
	ListenAddr  string `yaml:"listen,omitempty"`
	AgentID     string `yaml:"agent_id,omitempty"`
	TargetHost  string `yaml:"target_host,omitempty"`
	TargetPort  int    `yaml:"target_port,omitempty"`
	AuthMode    string `yaml:"auth_mode,omitempty"`
	AllowRemote bool   `yaml:"allow_remote,omitempty"`
	AuthURL     string `yaml:"auth_url,omitempty"`
}

// documentYAML is the read-side view of the tray-owned keys. Unknown keys are
// ignored on decode, which is what lets a file carrying storage and pool tuning load
// without the tray having to model them.
type documentYAML struct {
	Mode   string `yaml:"mode"`
	Client struct {
		ServerURL string       `yaml:"server_url"`
		Token     string       `yaml:"token"`
		Tunnels   []tunnelYAML `yaml:"tunnels"`
	} `yaml:"client"`
}

// Load returns the tray-owned settings, or the documented defaults when the file is
// absent.
//
// A missing file is not an error: the tray has to open and offer an editable form
// before a client has ever been configured.
func (s *ConfigStore) Load() (ClientSettings, error) {
	settings := ClientSettings{Mode: config.ModeLocal}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return settings, nil
		}
		return ClientSettings{}, fmt.Errorf("read client configuration: %w", err)
	}
	var document documentYAML
	if err := yaml.Unmarshal(data, &document); err != nil {
		return ClientSettings{}, fmt.Errorf("parse client configuration: %w", err)
	}
	settings.Mode = document.Mode
	if settings.Mode == "" {
		settings.Mode = config.ModeLocal
	}
	settings.ServerURL = document.Client.ServerURL
	settings.Token = document.Client.Token
	for _, tunnel := range document.Client.Tunnels {
		settings.Tunnels = append(settings.Tunnels, config.TunnelConfig{
			Name: tunnel.Name, Protocol: tunnel.Protocol, ListenAddr: tunnel.ListenAddr,
			AgentID: tunnel.AgentID, TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
			AuthMode: tunnel.AuthMode, AllowRemote: tunnel.AllowRemote, AuthURL: tunnel.AuthURL,
		})
	}
	return settings, nil
}

// Save writes the tray-owned keys back into client.yaml, preserving every other key
// and every comment.
//
// Editing a node tree rather than re-marshalling a struct is the whole point: this
// file is shared with `client run`, and an operator's storage block, pool tuning and
// notes must survive a trip through the settings window. The file is written with
// owner-only permissions because it carries the token.
func (s *ConfigStore) Save(settings ClientSettings) error {
	document, mapping, err := s.loadDocument()
	if err != nil {
		return err
	}
	mode := settings.Mode
	if mode == "" {
		mode = config.ModeLocal
	}
	// Every edit targets the root mapping, never the document node: a document node
	// holds exactly one child, and appending keys to it produces a tree the emitter
	// rejects.
	if err := setNodeValue(mapping, "mode", mode); err != nil {
		return err
	}
	clientNode := ensureMappingNode(mapping, "client")
	if err := setNodeValue(clientNode, "server_url", settings.ServerURL); err != nil {
		return err
	}
	if err := setNodeValue(clientNode, "token", settings.Token); err != nil {
		return err
	}
	tunnels := make([]tunnelYAML, 0, len(settings.Tunnels))
	for _, tunnel := range settings.Tunnels {
		tunnels = append(tunnels, tunnelYAML{
			Name: tunnel.Name, Protocol: tunnel.Protocol, ListenAddr: tunnel.ListenAddr,
			AgentID: tunnel.AgentID, TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
			AuthMode: tunnel.AuthMode, AllowRemote: tunnel.AllowRemote, AuthURL: tunnel.AuthURL,
		})
	}
	if err := setNodeValue(clientNode, "tunnels", tunnels); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("render client configuration: %w", err)
	}
	return atomicWrite(s.path, data, 0o600)
}

// LoadConfig runs the real loader and validator over the file.
//
// The tray validates through config.Load rather than reimplementing the rules so a
// configuration it accepts is a configuration `client run` accepts. That includes the
// mode/storage coupling, which is why choosing cluster mode without MySQL storage has
// to be reported here instead of surfacing as a crash at launch.
func (s *ConfigStore) LoadConfig(ctx context.Context) (config.Config, error) {
	if !s.Exists() {
		return config.Config{}, fmt.Errorf("client configuration %s does not exist", s.path)
	}
	return config.Load(ctx, config.ConfigOptions{ConfigFile: s.path})
}

// loadDocument returns the document to marshal and the root mapping to edit, or a
// fresh empty pair when the file is absent, empty or not a mapping.
//
// Both are returned because they are not interchangeable: the document node is what
// yaml.Marshal wants, while every key edit has to land on the mapping inside it.
func (s *ConfigStore) loadDocument() (*yaml.Node, *yaml.Node, error) {
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping}}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return document, mapping, nil
		}
		return nil, nil, fmt.Errorf("read client configuration: %w", err)
	}
	if len(data) == 0 {
		return document, mapping, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse client configuration: %w", err)
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 && root.Content[0].Kind == yaml.MappingNode {
		return &root, root.Content[0], nil
	}
	// A file whose top level is not a mapping cannot hold client configuration. It is
	// replaced rather than merged, because merging into a scalar has no meaning.
	return document, mapping, nil
}

// mappingValueNode returns the value node stored under key, or nil.
func mappingValueNode(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// ensureMappingNode returns the mapping stored under key, creating it when absent and
// replacing it when it holds something else.
func ensureMappingNode(mapping *yaml.Node, key string) *yaml.Node {
	if existing := mappingValueNode(mapping, key); existing != nil {
		if existing.Kind == yaml.MappingNode {
			return existing
		}
		existing.Kind = yaml.MappingNode
		existing.Tag = "!!map"
		existing.Value = ""
		existing.Style = 0
		existing.Content = nil
		return existing
	}
	created := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		created,
	)
	return created
}

// setNodeValue stores value under key, replacing any existing entry in place.
//
// Replacing in place rather than appending a new pair keeps the key node, and with it
// any comment an operator attached above the key. The value node's own comments are
// carried over for the same reason.
func setNodeValue(mapping *yaml.Node, key string, value any) error {
	var encoded yaml.Node
	if err := encoded.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	if isEmptySequence(value, &encoded) {
		encoded = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	existing := mappingValueNode(mapping, key)
	if existing == nil {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&encoded,
		)
		return nil
	}
	head, line, foot := existing.HeadComment, existing.LineComment, existing.FootComment
	*existing = encoded
	existing.HeadComment, existing.LineComment, existing.FootComment = head, line, foot
	return nil
}

// isEmptySequence reports whether an encoded node is YAML null produced by an empty
// slice. Writing `tunnels: null` is technically loadable but reads as a mistake, so an
// empty list is rendered as an empty sequence instead.
func isEmptySequence(value any, encoded *yaml.Node) bool {
	if encoded.Kind == yaml.SequenceNode {
		return len(encoded.Content) == 0
	}
	switch typed := value.(type) {
	case []tunnelYAML:
		return len(typed) == 0
	default:
		return false
	}
}

// BuildConfig applies pending settings on top of the stored file and runs the real
// loader and validator, without writing anything to disk.
//
// Loading through config.Load rather than assembling a Config by hand matters: the
// loader fills the documented defaults, and client.connections.min has to be at least
// 1 for validation to pass. A hand-built struct would report a configuration invalid
// that `client run` accepts, and the interface would refuse to launch a working client.
func (s *ConfigStore) BuildConfig(ctx context.Context, settings ClientSettings) (config.Config, error) {
	mode := settings.Mode
	if mode == "" {
		mode = config.ModeLocal
	}
	opts := config.ConfigOptions{Overrides: map[string]any{
		"mode":              mode,
		"client.server_url": settings.ServerURL,
		"client.token":      settings.Token,
		"client.tunnels":    settings.Tunnels,
	}}
	// The stored file is the base so pool tuning and stream buffers the tray does not
	// edit still take part in validation.
	if s.Exists() {
		opts.ConfigFile = s.path
	}
	return config.Load(ctx, opts)
}
