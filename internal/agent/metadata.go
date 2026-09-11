package agent

import (
	"context"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/metadata"
)

const (
	DefaultMetadataMaxFields    = config.MetadataMaxFields
	DefaultMetadataFieldBytes   = config.MetadataFieldMaxBytes
	DefaultMetadataPayloadBytes = config.MetadataPayloadMaxBytes
)

// Public aliases preserve the Agent package boundary while delegating the
// allowlist contract and sensitive-name policy to the shared metadata package.
type (
	MetadataField      = metadata.Field
	MetadataFieldError = metadata.FieldError
	MetadataSnapshot   = metadata.Snapshot
)

// MetadataCollector reads only explicitly configured file and environment
// sources. A source failure becomes a field error, while policy and aggregate
// limit violations fail the collection before an oversized snapshot escapes.
type MetadataCollector struct {
	*metadata.Collector
}

func NewMetadataCollector(sources []config.MetadataSource) *MetadataCollector {
	return &MetadataCollector{Collector: metadata.NewCollector(sources)}
}

func (c *MetadataCollector) Collect(ctx context.Context) (MetadataSnapshot, error) {
	if c == nil {
		return metadata.Snapshot{Values: map[string]string{}}, nil
	}
	return c.Collector.Collect(ctx)
}
