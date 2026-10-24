package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func tunnelSortCursorValue(tunnel storage.Tunnel, column string) string {
	switch column {
	case "domain":
		return tunnel.Domain
	case "updated_at":
		return tunnel.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		return tunnel.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
}

type ListSort struct {
	Column     string
	Descending bool
}

func (s ListSort) Spec() storage.SortSpec {
	return storage.SortSpec{Column: s.Column, Descending: s.Descending, Composite: s.Column != "id"}
}

type listQueryError struct {
	Message string
	Allowed []string
}

func newListQueryError(message string, allowed []string) *listQueryError {
	return &listQueryError{Message: message, Allowed: append([]string(nil), allowed...)}
}

func (e *listQueryError) write(w http.ResponseWriter) {
	if e == nil {
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": e.Message, "allowed": e.Allowed})
}

func parseListSort(r *http.Request, allowed map[string]string, defaultColumn string) (ListSort, *listQueryError) {
	name := strings.TrimSpace(r.URL.Query().Get("sort"))
	if name == "" {
		column := allowed[defaultColumn]
		if column == "" {
			column = defaultColumn
		}
		return ListSort{Column: column, Descending: true}, nil
	}
	column, ok := allowed[name]
	if !ok {
		return ListSort{}, newListQueryError("unsupported sort column", allowlistKeys(allowed))
	}
	direction := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("order")))
	if direction == "" || direction == "desc" {
		return ListSort{Column: column, Descending: true}, nil
	}
	if direction == "asc" {
		return ListSort{Column: column}, nil
	}
	return ListSort{}, newListQueryError("order must be asc or desc", []string{"asc", "desc"})
}

func queryMulti(r *http.Request, key string, allowed map[string]string) ([]string, *listQueryError) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		canonical, ok := containsFold(allowed, item)
		if !ok {
			return nil, newListQueryError("unsupported filter value", allowlistKeys(allowed))
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		values = append(values, canonical)
	}
	return values, nil
}

func containsFold(values map[string]string, candidate string) (string, bool) {
	for key, value := range values {
		if strings.EqualFold(key, candidate) {
			return value, true
		}
	}
	return "", false
}

func allowlistKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
