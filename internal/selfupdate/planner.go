package selfupdate

import (
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

// Planner decides whether an upgrade is allowed and to which version.
type Planner struct {
	// Current is the version the worker is running right now (from its ready
	// file), not the supervisor's own build: after an upgrade the two differ.
	Current string
	State   State
	Now     func() time.Time
}

// Decision is the Planner's answer.
type Decision struct {
	Upgrade bool
	Target  semver.Version
	// Reason says why not, for the log.
	Reason string
}

// Decide applies the upgrade policy:
//
//   - the target is min(latest release, Server version), so a client or agent never
//     runs ahead of the Server it talks to;
//   - it never downgrades, never upgrades a development build, and never upgrades
//     when the Server's version is unknown;
//   - a stable install does not move onto a pre-release;
//   - a version that failed an upgrade recently is skipped until its mark expires.
func (p Planner) Decide(latest Release, serverVersion string) Decision {
	current, err := semver.Parse(p.Current)
	if err != nil {
		return Decision{Reason: "running a development or unknown build; upgrades apply to releases only"}
	}
	server, err := semver.Parse(serverVersion)
	if err != nil {
		return Decision{Reason: "the Server version is unknown, so the upgrade ceiling cannot be determined"}
	}
	target := semver.Min(latest.Version, server)
	if target.Compare(current) <= 0 {
		return Decision{Reason: "already at or above the newest version the Server allows"}
	}
	if target.IsPre() && !current.IsPre() {
		return Decision{Reason: "the target is a pre-release and this install is on a stable release"}
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	if p.State.IsBad(target.String(), now()) {
		return Decision{Reason: "version " + target.String() + " failed an upgrade recently and is not retried yet"}
	}
	return Decision{Upgrade: true, Target: target}
}
