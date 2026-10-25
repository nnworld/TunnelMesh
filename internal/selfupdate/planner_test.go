package selfupdate

import (
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

func v(t *testing.T, s string) semver.Version {
	t.Helper()
	parsed, err := semver.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	badNow := State{}
	badNow.MarkBad("v1.4.0", "never ready", now.Add(-time.Hour), 2*time.Hour)
	expiredBad := State{}
	expiredBad.MarkBad("v1.4.0", "never ready", now.Add(-3*time.Hour), time.Hour)

	cases := []struct {
		name          string
		current       string
		latest        string
		server        string
		state         State
		want          string // target, or empty for "no upgrade"
		reasonContain string
	}{
		{name: "newer release and Server allows it", current: "v1.3.0", latest: "v1.4.0", server: "v1.4.0", want: "v1.4.0"},
		{name: "Server is the ceiling", current: "v1.3.0", latest: "v1.5.0", server: "v1.4.0", want: "v1.4.0"},
		{name: "release is the ceiling", current: "v1.3.0", latest: "v1.4.0", server: "v1.9.0", want: "v1.4.0"},
		{name: "already current", current: "v1.4.0", latest: "v1.4.0", server: "v1.4.0", reasonContain: "already"},
		{name: "never downgrades to the Server", current: "v1.5.0", latest: "v1.5.0", server: "v1.4.0", reasonContain: "already"},
		{name: "a development build is left alone", current: "dev", latest: "v1.4.0", server: "v1.4.0", reasonContain: "development"},
		{name: "an empty version is left alone", current: "", latest: "v1.4.0", server: "v1.4.0", reasonContain: "development"},
		{name: "Server version unknown", current: "v1.3.0", latest: "v1.4.0", server: "", reasonContain: "Server version"},
		{name: "Server is a development build", current: "v1.3.0", latest: "v1.4.0", server: "dev", reasonContain: "Server version"},
		{name: "stable never moves onto a pre-release", current: "v1.3.0", latest: "v1.4.0-pre", server: "v1.4.0-pre", reasonContain: "pre-release"},
		{name: "a pre-release may follow pre-releases", current: "v1.3.0-pre", latest: "v1.4.0-pre", server: "v1.4.0-pre", want: "v1.4.0-pre"},
		{name: "a pre-release may move to the stable release", current: "v1.4.0-pre", latest: "v1.4.0", server: "v1.4.0", want: "v1.4.0"},
		{name: "a version that failed recently is skipped", current: "v1.3.0", latest: "v1.4.0", server: "v1.4.0", state: badNow, reasonContain: "failed"},
		{name: "a failed version is retried after its TTL", current: "v1.3.0", latest: "v1.4.0", server: "v1.4.0", state: expiredBad, want: "v1.4.0"},
		{name: "a failure of another version does not matter", current: "v1.3.0", latest: "v1.4.0", server: "v1.4.0", state: func() State {
			s := State{}
			s.MarkBad("v1.3.5", "x", now, time.Hour)
			return s
		}(), want: "v1.4.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Planner{Current: tc.current, State: tc.state, Now: func() time.Time { return now }}
			latest := Release{}
			if tc.latest != "" {
				latest = Release{Version: v(t, tc.latest), Tag: tc.latest}
			}
			got := p.Decide(latest, tc.server)
			if tc.want == "" {
				if got.Upgrade {
					t.Fatalf("Decide() = %+v, want no upgrade", got)
				}
				if !strings.Contains(got.Reason, tc.reasonContain) {
					t.Fatalf("Reason = %q, want it to mention %q", got.Reason, tc.reasonContain)
				}
				return
			}
			if !got.Upgrade || got.Target.String() != tc.want {
				t.Fatalf("Decide() = %+v, want an upgrade to %s", got, tc.want)
			}
		})
	}
}
