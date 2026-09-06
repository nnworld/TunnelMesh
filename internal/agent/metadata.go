package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

const (
	DefaultMetadataMaxFields    = config.MetadataMaxFields
	DefaultMetadataFieldBytes   = config.MetadataFieldMaxBytes
	DefaultMetadataPayloadBytes = config.MetadataPayloadMaxBytes
)

// MetadataField is one successfully collected allowlisted value.
type MetadataField struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

// MetadataFieldError records a source failure without exposing a path, key, or
// value. Errors are intentionally field-scoped so data forwarding can continue.
type MetadataFieldError struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Error  string `json:"error"`
}

// MetadataSnapshot is the deterministic collection result. Fields are sorted
// by name; Values is a convenience lookup for service and protocol callers.
type MetadataSnapshot struct {
	Fields       []MetadataField      `json:"fields"`
	Values       map[string]string    `json:"values"`
	Errors       []MetadataFieldError `json:"errors,omitempty"`
	PayloadBytes int                  `json:"payload_bytes"`
}

// MetadataCollector reads only explicitly configured file and environment
// sources. A source failure becomes a field error, while policy and aggregate
// limit violations fail the collection before an oversized snapshot escapes.
type MetadataCollector struct {
	Sources         []config.MetadataSource
	MaxFields       int
	MaxFieldBytes   int
	MaxPayloadBytes int
}

func NewMetadataCollector(sources []config.MetadataSource) *MetadataCollector {
	return &MetadataCollector{
		Sources:         append([]config.MetadataSource(nil), sources...),
		MaxFields:       DefaultMetadataMaxFields,
		MaxFieldBytes:   DefaultMetadataFieldBytes,
		MaxPayloadBytes: DefaultMetadataPayloadBytes,
	}
}

func (c *MetadataCollector) Collect(ctx context.Context) (MetadataSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return MetadataSnapshot{}, err
	}
	if c == nil {
		return MetadataSnapshot{Values: map[string]string{}}, nil
	}
	maxFields, maxFieldBytes, maxPayloadBytes := c.limits()
	if len(c.Sources) > maxFields {
		return MetadataSnapshot{}, fmt.Errorf("metadata field count exceeds %d", maxFields)
	}
	sources := append([]config.MetadataSource(nil), c.Sources...)
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].Name == sources[j].Name {
			return sources[i].Source < sources[j].Source
		}
		return sources[i].Name < sources[j].Name
	})
	snapshot := MetadataSnapshot{Values: make(map[string]string), Fields: make([]MetadataField, 0, len(sources))}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		if err := validateCollectorSource(source, seen); err != nil {
			snapshot.Errors = append(snapshot.Errors, MetadataFieldError{Name: source.Name, Source: source.Source, Error: err.Error()})
			continue
		}
		seen[source.Name] = struct{}{}
		value, err := readMetadataSource(ctx, source, maxFieldBytes)
		if err != nil {
			snapshot.Errors = append(snapshot.Errors, MetadataFieldError{Name: source.Name, Source: source.Source, Error: err.Error()})
			continue
		}
		snapshot.Values[source.Name] = value
		snapshot.Fields = append(snapshot.Fields, MetadataField{Name: source.Name, Source: source.Source, Value: value})
	}
	sort.Slice(snapshot.Errors, func(i, j int) bool { return snapshot.Errors[i].Name < snapshot.Errors[j].Name })
	if payload, err := json.Marshal(snapshot.Fields); err == nil {
		snapshot.PayloadBytes = len(payload)
		if snapshot.PayloadBytes > maxPayloadBytes {
			return snapshot, fmt.Errorf("metadata payload exceeds %d bytes", maxPayloadBytes)
		}
	}
	return snapshot, nil
}

func (c *MetadataCollector) limits() (int, int, int) {
	maxFields, maxFieldBytes, maxPayloadBytes := c.MaxFields, c.MaxFieldBytes, c.MaxPayloadBytes
	if maxFields <= 0 {
		maxFields = DefaultMetadataMaxFields
	}
	if maxFieldBytes <= 0 {
		maxFieldBytes = DefaultMetadataFieldBytes
	}
	if maxPayloadBytes <= 0 {
		maxPayloadBytes = DefaultMetadataPayloadBytes
	}
	return maxFields, maxFieldBytes, maxPayloadBytes
}

var collectorNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var collectorSensitivePattern = regexp.MustCompile(`(?i)(password|token|secret|private[_-]?key|dsn)`)
var errMetadataFieldTooLarge = errors.New("metadata field exceeds configured limit")

func validateCollectorSource(source config.MetadataSource, seen map[string]struct{}) error {
	if source.Name == "" || len(source.Name) > 64 || !collectorNamePattern.MatchString(source.Name) {
		return errors.New("invalid metadata name")
	}
	if collectorSensitivePattern.MatchString(source.Name) {
		return errors.New("sensitive metadata name")
	}
	if _, ok := seen[source.Name]; ok {
		return errors.New("duplicate metadata name")
	}
	switch source.Source {
	case "file":
		if source.Path == "" || !strings.HasPrefix(source.Path, "/") {
			return errors.New("file path must be absolute")
		}
	case "env":
		if source.Key == "" {
			return errors.New("env key is required")
		}
		if strings.ContainsAny(source.Key, "*?[]") {
			return errors.New("env key cannot contain wildcard")
		}
	default:
		return errors.New("source must be file or env")
	}
	return nil
}

func readMetadataSource(ctx context.Context, source config.MetadataSource, maxBytes int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var data []byte
	var err error
	switch source.Source {
	case "file":
		file, openErr := os.Open(source.Path)
		if openErr != nil {
			return "", errors.New("source read failed")
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	case "env":
		value, exists := os.LookupEnv(source.Key)
		if !exists {
			return "", errors.New("environment value is missing")
		}
		// Check the string's byte length before converting it to a byte slice;
		// oversized environment values must not trigger an avoidable copy.
		if len(value) > maxBytes {
			return "", errMetadataFieldTooLarge
		}
		data = []byte(value)
	}
	if err != nil {
		return "", errors.New("source read failed")
	}
	if len(data) > maxBytes {
		return "", errMetadataFieldTooLarge
	}
	if !utf8.Valid(data) {
		return "", errors.New("metadata value is not valid UTF-8")
	}
	return trimOneTrailingNewline(string(data)), nil
}

func trimOneTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
}
