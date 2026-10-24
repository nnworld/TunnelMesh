package storage

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// SortSpec describes one allowlisted SQL sort column and its direction. The
// caller must only construct Column from a server-side allowlist; repositories
// use OrderBy to keep user input out of SQL identifiers.
type SortSpec struct {
	Column     string
	Descending bool
	Composite  bool
}

// EncodeCursor is the opaque cursor format shared by cursor list endpoints.
// Keeping encoding here lets services that filter rows after a repository page
// still return a cursor in exactly the format the repository consumes.
func EncodeCursor(value string) string {
	if value == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// NextCursor encodes the final row of a sorted page using the same key shape as
// CursorCondition. ID-only ordering has one key; every other ordering carries
// the primary value and the stable ID tie-breaker.
func (s SortSpec) NextCursor(sortValue, id string) string {
	if s.Column == "id" || !s.Composite {
		return EncodeCursor(id)
	}
	if id == "" {
		return ""
	}
	return EncodeCursor(sortValue + "\x00" + id)
}

func (s SortSpec) OrderBy() string {
	direction := "ASC"
	if s.Descending {
		direction = "DESC"
	}
	if s.Column == "" || s.Column == "id" {
		return "id " + direction
	}
	return s.Column + " " + direction + ", id " + direction
}

// CursorCondition returns the seek predicate for a cursor created from the
// last row of a page. Composite timestamps need the ID tie-breaker to avoid
// skipping rows with equal timestamps.
func (s SortSpec) CursorCondition(sortValue, id string) (string, []any) {
	operator := ">"
	if s.Descending {
		operator = "<"
	}
	if s.Column == "" || s.Column == "id" || !s.Composite {
		return "id " + operator + " ?", []any{id}
	}
	return fmt.Sprintf("(%s %s ? OR (%s = ? AND id %s ?))", s.Column, operator, s.Column, operator), []any{sortValue, sortValue, id}
}

func EncodeSortCursor(sortValue time.Time, id string) string {
	if sortValue.IsZero() || id == "" {
		return ""
	}
	payload := sortValue.UTC().Format(time.RFC3339Nano) + "\x00" + id
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func DecodeSortCursor(cursor string) (time.Time, string, bool) {
	if cursor == "" {
		return time.Time{}, "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}
	timestamp, id, ok := strings.Cut(string(payload), "\x00")
	if !ok || id == "" {
		return time.Time{}, "", false
	}
	value, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Time{}, "", false
	}
	return value, id, true
}
