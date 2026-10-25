package semver

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	valid := []struct {
		in   string
		want Version
	}{
		{"v1.2.3", Version{1, 2, 3, ""}},
		{"1.2.3", Version{1, 2, 3, ""}},
		{"v10.0.1-pre", Version{10, 0, 1, "pre"}},
		{"  v0.3.0  ", Version{0, 3, 0, ""}},
	}
	for _, tc := range valid {
		got, err := Parse(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Parse(%q) = %+v, %v, want %+v", tc.in, got, err, tc.want)
		}
	}
	// "dev" is what a plain `go build` reports; it must never be mistaken for a release.
	for _, in := range []string{"dev", "", "unknown", "v1.2", "v1.2.3.4", "va.b.c", "v1.2.3-", "v-1.2.3", "v1.2.3+meta"} {
		if _, err := Parse(in); !errors.Is(err, ErrNotRelease) {
			t.Errorf("Parse(%q) error = %v, want ErrNotRelease", in, err)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.10.0", "v1.9.0", 1},
		{"v2.0.0", "v1.99.99", 1},
		// A pre-release sorts below the release of the same triple.
		{"v1.2.3-pre", "v1.2.3", -1},
		{"v1.2.3", "v1.2.3-pre", 1},
		{"v1.2.3-pre", "v1.2.3-pre", 0},
		{"v1.2.4-pre", "v1.2.3", 1},
	}
	for _, tc := range cases {
		a, b := mustParse(t, tc.a), mustParse(t, tc.b)
		if got := a.Compare(b); got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStringAndHelpers(t *testing.T) {
	if got := mustParse(t, "1.2.3").String(); got != "v1.2.3" {
		t.Fatalf("String() = %q, want v1.2.3", got)
	}
	if got := mustParse(t, "v1.2.3-pre").String(); got != "v1.2.3-pre" {
		t.Fatalf("String() = %q, want v1.2.3-pre", got)
	}
	if !mustParse(t, "v1.2.3-pre").IsPre() || mustParse(t, "v1.2.3").IsPre() {
		t.Fatal("IsPre() misclassified")
	}
	if got := Min(mustParse(t, "v1.4.0"), mustParse(t, "v1.3.5")); got != mustParse(t, "v1.3.5") {
		t.Fatalf("Min() = %v, want v1.3.5", got)
	}
}

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}
