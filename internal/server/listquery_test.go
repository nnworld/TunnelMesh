package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestParseListSortDefaultsToDescending(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?sort=", nil)
	sort, err := parseListSort(r, map[string]string{"createdAt": "created_at"}, "created_at")
	if err != nil || sort.Column != "created_at" || !sort.Descending {
		t.Fatalf("parseListSort() = %#v, %v", sort, err)
	}
}

func TestParseListSortMapsDefaultAliasToSQLColumn(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	sort, err := parseListSort(r, map[string]string{"updatedAt": "updated_at"}, "updatedAt")
	if err != nil {
		t.Fatalf("parseListSort() error = %v", err)
	}
	if sort.Column != "updated_at" || !sort.Descending {
		t.Fatalf("parseListSort() = %#v, want updated_at descending", sort)
	}
}

func TestTunnelSortCursorValueUsesRequestedColumn(t *testing.T) {
	tunnel := storage.Tunnel{
		ID: "route-1", Domain: "example.test", CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}
	if got := tunnelSortCursorValue(tunnel, "domain"); got != "example.test" {
		t.Fatalf("domain cursor value = %q", got)
	}
	if got := tunnelSortCursorValue(tunnel, "updated_at"); got == "" || got == "example.test" {
		t.Fatalf("updated cursor value = %q", got)
	}
}

func TestParseListSortRejectsUnknownSortAndOrder(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?sort=createdAt&order=sideways", nil)
	_, err := parseListSort(r, map[string]string{"createdAt": "created_at"}, "created_at")
	if err == nil || err.Message != "order must be asc or desc" {
		t.Fatalf("parseListSort() error = %#v", err)
	}

	r = httptest.NewRequest(http.MethodGet, "/?sort=unknown", nil)
	_, err = parseListSort(r, map[string]string{"createdAt": "created_at"}, "created_at")
	if err == nil || err.Message != "unsupported sort column" {
		t.Fatalf("parseListSort() unknown error = %#v", err)
	}
}

func TestQueryMultiNormalizesAndDeduplicatesValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?status=Active,active,DELETED", nil)
	values, err := queryMulti(r, "status", map[string]string{"active": "active", "deleted": "deleted"})
	if err != nil {
		t.Fatalf("queryMulti() error = %#v", err)
	}
	if want := []string{"active", "deleted"}; !reflect.DeepEqual(values, want) {
		t.Fatalf("queryMulti() = %#v, want %#v", values, want)
	}
}

func TestQueryMultiRejectsUnknownValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?status=active,broken", nil)
	if _, err := queryMulti(r, "status", map[string]string{"active": "active"}); err == nil {
		t.Fatal("queryMulti() accepted an unknown value")
	}
}

func TestQueryMultiDeduplicatesAndValidatesValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?status=active,ACTIVE,disabled", nil)
	values, err := queryMulti(r, "status", map[string]string{"active": "active", "disabled": "disabled"})
	if err != nil || len(values) != 2 || values[0] != "active" || values[1] != "disabled" {
		t.Fatalf("queryMulti() = %#v, %v", values, err)
	}

	r = httptest.NewRequest(http.MethodGet, "/?status=unknown", nil)
	if _, err := queryMulti(r, "status", map[string]string{"active": "active"}); err == nil {
		t.Fatal("queryMulti() accepted an unknown value")
	}
}
