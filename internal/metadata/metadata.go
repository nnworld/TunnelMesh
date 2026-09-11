// Package metadata owns the shared allowlist source contract used by Agent
// and Client runtime metadata. Keeping collection here prevents the two
// executables from drifting on sensitive-name or file/environment rules.
package metadata

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
)

const (
	MaxFields       = 32
	MaxFieldBytes   = 4 << 10
	MaxPayloadBytes = 32 << 10
)

// Source is an explicitly allowlisted host value source.
type Source struct {
	Name   string `mapstructure:"name" json:"name" yaml:"name"`
	Source string `mapstructure:"source" json:"source" yaml:"source"`
	Path   string `mapstructure:"path" json:"path" yaml:"path"`
	Key    string `mapstructure:"key" json:"key" yaml:"key"`
}

// Field is one successfully collected allowlisted value.
type Field struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

// FieldError records a source failure without exposing a path, key, or value.
type FieldError struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Error  string `json:"error"`
}

// Snapshot is the deterministic collection result.
type Snapshot struct {
	Fields       []Field           `json:"fields"`
	Values       map[string]string `json:"values"`
	Errors       []FieldError      `json:"errors,omitempty"`
	PayloadBytes int               `json:"payload_bytes"`
}

// Collector reads only configured file and environment sources.
type Collector struct {
	Sources         []Source
	MaxFields       int
	MaxFieldBytes   int
	MaxPayloadBytes int
}

func NewCollector(sources []Source) *Collector {
	return &Collector{
		Sources: append([]Source(nil), sources...), MaxFields: MaxFields,
		MaxFieldBytes: MaxFieldBytes, MaxPayloadBytes: MaxPayloadBytes,
	}
}

func (c *Collector) Collect(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if c == nil {
		return Snapshot{Values: map[string]string{}}, nil
	}
	maxFields, maxFieldBytes, maxPayloadBytes := c.limits()
	if len(c.Sources) > maxFields {
		return Snapshot{}, fmt.Errorf("metadata field count exceeds %d", maxFields)
	}
	sources := append([]Source(nil), c.Sources...)
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].Name == sources[j].Name {
			return sources[i].Source < sources[j].Source
		}
		return sources[i].Name < sources[j].Name
	})
	snapshot := Snapshot{Values: make(map[string]string, len(sources)), Fields: make([]Field, 0, len(sources))}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		if err := ValidateSource(source, seen); err != nil {
			snapshot.Errors = append(snapshot.Errors, FieldError{Name: source.Name, Source: source.Source, Error: err.Error()})
			continue
		}
		seen[source.Name] = struct{}{}
		value, err := ReadSource(ctx, source, maxFieldBytes)
		if err != nil {
			snapshot.Errors = append(snapshot.Errors, FieldError{Name: source.Name, Source: source.Source, Error: err.Error()})
			continue
		}
		snapshot.Values[source.Name] = value
		snapshot.Fields = append(snapshot.Fields, Field{Name: source.Name, Source: source.Source, Value: value})
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

func (c *Collector) limits() (int, int, int) {
	maxFields, maxFieldBytes, maxPayloadBytes := c.MaxFields, c.MaxFieldBytes, c.MaxPayloadBytes
	if maxFields <= 0 {
		maxFields = MaxFields
	}
	if maxFieldBytes <= 0 {
		maxFieldBytes = MaxFieldBytes
	}
	if maxPayloadBytes <= 0 {
		maxPayloadBytes = MaxPayloadBytes
	}
	return maxFields, maxFieldBytes, maxPayloadBytes
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var sensitivePattern = regexp.MustCompile(`(?i)(password|passphrase|token|secret|private[_-]?key|api[_-]?key|credential|authorization|cookie|dsn)`)
var ErrFieldTooLarge = errors.New("metadata field exceeds configured limit")

func ValidateSource(source Source, seen map[string]struct{}) error {
	if err := ValidateName(source.Name); err != nil {
		return err
	}
	if _, ok := seen[source.Name]; ok {
		return errors.New("duplicate metadata name")
	}
	switch source.Source {
	case "file":
		if source.Path == "" || !isAbsolutePath(source.Path) {
			return errors.New("file path must be absolute")
		}
		if source.Key != "" {
			return errors.New("file source cannot set env key")
		}
	case "env":
		if source.Key == "" {
			return errors.New("env key is required")
		}
		if strings.ContainsAny(source.Key, "*?[]") {
			return errors.New("env key cannot contain wildcard")
		}
		if source.Path != "" {
			return errors.New("env source cannot set file path")
		}
	default:
		return errors.New("source must be file or env")
	}
	return nil
}

// ValidateName applies the shared name policy to pre-collected values. Source
// validation remains stricter because collected fields can also be static.
func ValidateName(name string) error {
	if name == "" || len(name) > 64 || !namePattern.MatchString(name) {
		return errors.New("invalid metadata name")
	}
	if sensitivePattern.MatchString(name) {
		return errors.New("sensitive metadata name")
	}
	return nil
}

func ReadSource(ctx context.Context, source Source, maxBytes int) (string, error) {
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
		if len(value) > maxBytes {
			return "", ErrFieldTooLarge
		}
		data = []byte(value)
	default:
		return "", errors.New("source must be file or env")
	}
	if err != nil {
		return "", errors.New("source read failed")
	}
	if len(data) > maxBytes {
		return "", ErrFieldTooLarge
	}
	if !utf8.Valid(data) {
		return "", errors.New("metadata value is not valid UTF-8")
	}
	return trimOneTrailingNewline(string(data)), nil
}

func isAbsolutePath(path string) bool {
	return strings.HasPrefix(path, "/") || (len(path) > 2 && path[1] == ':')
}

func trimOneTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
}
