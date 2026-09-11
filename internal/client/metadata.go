package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/metadata"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

const (
	DefaultMetadataMaxFields    = config.MetadataMaxFields
	DefaultMetadataFieldBytes   = config.MetadataFieldMaxBytes
	DefaultMetadataPayloadBytes = config.MetadataPayloadMaxBytes
)

// MetadataField is a caller-supplied, non-secret client annotation.
type MetadataField struct {
	Name   string
	Source string
	Value  string
}

// ClientMetadataOptions are shared by every physical connection slot. The
// collector copies them so later mutation cannot bypass validation.
type ClientMetadataOptions struct {
	InstanceID     string
	AgentIDs       []string
	ConnectionSlot int
	Version        string
	Commit         string
	Listeners      []protocol.ClientListener
	Metadata       []MetadataField
}

// ClientMetadataCollector creates deterministic, bounded metadata snapshots.
// It never reads credentials and never includes the token in a payload.
type ClientMetadataCollector struct {
	options        ClientMetadataOptions
	processStartAt time.Time
	platform       string

	mu       sync.Mutex
	revision uint64
	last     protocol.ClientMetadataPayload
}

func NewClientMetadataCollector(options ClientMetadataOptions) (*ClientMetadataCollector, error) {
	if strings.TrimSpace(options.InstanceID) == "" {
		return nil, errors.New("client metadata instance id is required")
	}
	if len(options.Metadata) > DefaultMetadataMaxFields {
		return nil, fmt.Errorf("client metadata field count exceeds %d", DefaultMetadataMaxFields)
	}
	seen := make(map[string]struct{}, len(options.Metadata))
	for _, field := range options.Metadata {
		if _, exists := seen[field.Name]; exists {
			return nil, fmt.Errorf("duplicate client metadata name %q", field.Name)
		}
		if err := validateClientMetadataField(field); err != nil {
			return nil, err
		}
		seen[field.Name] = struct{}{}
	}
	if err := validateClientMetadataPayloadBytes(options); err != nil {
		return nil, err
	}

	copied := cloneClientMetadataOptions(options)
	sort.Slice(copied.Metadata, func(i, j int) bool { return copied.Metadata[i].Name < copied.Metadata[j].Name })
	return &ClientMetadataCollector{
		options:        copied,
		processStartAt: time.Now().UTC(),
		platform:       runtime.GOOS + "/" + runtime.GOARCH,
	}, nil
}

// validateClientMetadataPayloadBytes builds a conservative wire-format
// estimate before the process starts. Reserving a maximum hostname keeps the
// check effective even though the real hostname is discovered later.
func validateClientMetadataPayloadBytes(options ClientMetadataOptions) error {
	payload := protocol.ClientMetadataPayload{
		InstanceID:     options.InstanceID,
		ConnectionSlot: options.ConnectionSlot,
		AgentIDs:       options.AgentIDs,
		Version:        options.Version,
		Commit:         options.Commit,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		Hostname:       strings.Repeat("h", 255),
		ProcessStartAt: time.Now().UTC(),
		ReportedAt:     time.Now().UTC(),
		Revision:       1,
		Listeners:      options.Listeners,
		Items:          make([]protocol.ClientMetadataItem, 0, len(options.Metadata)),
	}
	for _, field := range options.Metadata {
		payload.Items = append(payload.Items, protocol.ClientMetadataItem{
			Name: field.Name, Source: field.Source, Value: field.Value,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode client metadata payload: %w", err)
	}
	if len(encoded) > DefaultMetadataPayloadBytes {
		return fmt.Errorf("client metadata payload exceeds %d bytes", DefaultMetadataPayloadBytes)
	}
	return nil
}

func cloneClientMetadataOptions(options ClientMetadataOptions) ClientMetadataOptions {
	options.AgentIDs = append([]string(nil), options.AgentIDs...)
	options.Listeners = append([]protocol.ClientListener(nil), options.Listeners...)
	options.Metadata = append([]MetadataField(nil), options.Metadata...)
	return options
}

func (c *ClientMetadataCollector) Snapshot(ctx context.Context) protocol.ClientMetadataPayload {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return protocol.ClientMetadataPayload{}
	}
	if err := ctx.Err(); err != nil {
		return protocol.ClientMetadataPayload{}
	}

	hostname, hostnameErr := os.Hostname()
	payload := protocol.ClientMetadataPayload{
		InstanceID:     c.options.InstanceID,
		ConnectionSlot: c.options.ConnectionSlot,
		AgentIDs:       append([]string(nil), c.options.AgentIDs...),
		Version:        c.options.Version,
		Commit:         c.options.Commit,
		Platform:       c.platform,
		Hostname:       hostname,
		ProcessStartAt: c.processStartAt,
		ReportedAt:     time.Now().UTC(),
		Listeners:      append([]protocol.ClientListener(nil), c.options.Listeners...),
		Items:          make([]protocol.ClientMetadataItem, 0, len(c.options.Metadata)),
	}
	if hostnameErr != nil {
		payload.Errors = append(payload.Errors, protocol.ClientMetadataError{
			Name: "hostname", Code: "collect_failed", Message: "hostname lookup failed",
		})
	}
	for _, field := range c.options.Metadata {
		payload.Items = append(payload.Items, protocol.ClientMetadataItem{
			Name: field.Name, Source: field.Source, Value: field.Value,
		})
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revision == 0 || !sameClientMetadata(c.last, payload) {
		c.revision++
		payload.Revision = c.revision
		c.last = payload
	} else {
		payload.Revision = c.revision
	}
	return payload
}

func validateClientMetadataField(field MetadataField) error {
	if err := metadata.ValidateName(field.Name); err != nil {
		return fmt.Errorf("invalid client metadata field: %w", err)
	}
	if len(field.Value) > DefaultMetadataFieldBytes {
		return fmt.Errorf("client metadata field exceeds %d bytes", DefaultMetadataFieldBytes)
	}
	if !utf8.ValidString(field.Value) {
		return errors.New("client metadata value is not valid UTF-8")
	}
	return nil
}

func sameClientMetadata(left, right protocol.ClientMetadataPayload) bool {
	left.ReportedAt, right.ReportedAt = time.Time{}, time.Time{}
	left.Revision, right.Revision = 0, 0
	return left.InstanceID == right.InstanceID &&
		left.ConnectionSlot == right.ConnectionSlot &&
		left.Version == right.Version &&
		left.Commit == right.Commit &&
		left.Platform == right.Platform &&
		left.Hostname == right.Hostname &&
		left.ProcessStartAt.Equal(right.ProcessStartAt) &&
		stringSlicesEqual(left.AgentIDs, right.AgentIDs) &&
		listenersEqual(left.Listeners, right.Listeners) &&
		clientMetadataItemsEqual(left.Items, right.Items) &&
		clientMetadataErrorsEqual(left.Errors, right.Errors) &&
		stringSlicesEqual(left.Capabilities, right.Capabilities)
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func listenersEqual(left, right []protocol.ClientListener) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func clientMetadataItemsEqual(left, right []protocol.ClientMetadataItem) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func clientMetadataErrorsEqual(left, right []protocol.ClientMetadataError) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
