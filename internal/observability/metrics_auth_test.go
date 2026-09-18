package observability

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMetricsAuthFamilies asserts the six identity series are registered with the
// documented names and labels, and that each accessor increments or sets the
// series it is documented to touch.
func TestMetricsAuthFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	metrics.AuthLogin("password", "success")
	metrics.AuthLogin("oidc", "mfa_required")
	metrics.AuthMFAVerify("totp", "invalid")
	metrics.AuthMFAVerify("recovery", "attempts_exceeded")
	metrics.AuthOIDCStep("discovery", "ok")
	metrics.AuthOIDCStep("token", "error")
	metrics.SetAuthTrustedDevices(12)
	metrics.SetAuthPendingChallenges(3)
	metrics.SetAuthBlockedBuckets(1)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	labels := map[string][]string{}
	for _, family := range families {
		names[family.GetName()] = true
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				labels[family.GetName()] = append(labels[family.GetName()], label.GetName())
			}
			break
		}
	}
	// The exposition format sorts label pairs by name, so the documented
	// declaration order is normalized before comparison.
	for _, value := range labels {
		sort.Strings(value)
	}
	for _, name := range []string{
		"tunnelmesh_auth_login_total",
		"tunnelmesh_auth_mfa_verify_total",
		"tunnelmesh_auth_oidc_step_total",
		"tunnelmesh_auth_trusted_devices",
		"tunnelmesh_auth_pending_challenges",
		"tunnelmesh_auth_login_blocked_buckets",
	} {
		if !names[name] {
			t.Fatalf("missing metric family %s; registered = %v", name, names)
		}
	}
	if got := strings.Join(labels["tunnelmesh_auth_login_total"], ","); got != "method,result" {
		t.Fatalf("auth login labels = %q, want method,result", got)
	}
	if got := strings.Join(labels["tunnelmesh_auth_mfa_verify_total"], ","); got != "method,result" {
		t.Fatalf("auth mfa verify labels = %q, want method,result", got)
	}
	if got := strings.Join(labels["tunnelmesh_auth_oidc_step_total"], ","); got != "result,step" {
		t.Fatalf("auth oidc step labels = %q, want result,step", got)
	}

	if got := testutil.ToFloat64(metrics.authLoginTotal.WithLabelValues("password", "success")); got != 1 {
		t.Fatalf("password success = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.authLoginTotal.WithLabelValues("oidc", "mfa_required")); got != 1 {
		t.Fatalf("oidc mfa_required = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.authMFAVerifyTotal.WithLabelValues("recovery", "attempts_exceeded")); got != 1 {
		t.Fatalf("recovery attempts_exceeded = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.authOIDCStepTotal.WithLabelValues("token", "error")); got != 1 {
		t.Fatalf("oidc token error = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.authTrustedDevices.WithLabelValues()); got != 12 {
		t.Fatalf("trusted devices = %v, want 12", got)
	}
	if got := testutil.ToFloat64(metrics.authPendingChallenges.WithLabelValues()); got != 3 {
		t.Fatalf("pending challenges = %v, want 3", got)
	}
	if got := testutil.ToFloat64(metrics.authLoginBlockedBuckets.WithLabelValues()); got != 1 {
		t.Fatalf("blocked buckets = %v, want 1", got)
	}
}

// TestMetricsAuthLabelsAreBounded proves an unnormalizable value cannot create a
// new series. The login path is reachable by an unauthenticated caller, so an
// open label set would be an unbounded-cardinality denial of service against the
// metrics endpoint.
func TestMetricsAuthLabelsAreBounded(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	metrics.AuthLogin("attacker;drop", "some-novel-state")
	metrics.AuthMFAVerify(strings.Repeat("x", 500), "")
	metrics.AuthOIDCStep("", "weird")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() != "unknown" {
					t.Fatalf("%s label %s = %q, want unknown", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
	if got := testutil.ToFloat64(metrics.authLoginTotal.WithLabelValues("unknown", "unknown")); got != 1 {
		t.Fatalf("unknown login series = %v, want 1", got)
	}
}

// TestMetricsAuthGaugesRejectNegative proves a counting bug cannot publish a
// negative population, which would make a rate expression over the series
// meaningless.
func TestMetricsAuthGaugesRejectNegative(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.SetAuthTrustedDevices(-5)
	metrics.SetAuthPendingChallenges(-1)
	metrics.SetAuthBlockedBuckets(-100)
	if got := testutil.ToFloat64(metrics.authTrustedDevices.WithLabelValues()); got != 0 {
		t.Fatalf("trusted devices = %v, want 0", got)
	}
	if got := testutil.ToFloat64(metrics.authPendingChallenges.WithLabelValues()); got != 0 {
		t.Fatalf("pending challenges = %v, want 0", got)
	}
	if got := testutil.ToFloat64(metrics.authLoginBlockedBuckets.WithLabelValues()); got != 0 {
		t.Fatalf("blocked buckets = %v, want 0", got)
	}
}

// TestMetricsAuthNilReceiverIsSafe proves a deployment that did not wire the
// registry cannot panic on the login path. The handlers call these through an
// interface that may hold a typed nil.
func TestMetricsAuthNilReceiverIsSafe(t *testing.T) {
	var metrics *Metrics
	metrics.AuthLogin("password", "success")
	metrics.AuthMFAVerify("totp", "success")
	metrics.AuthOIDCStep("discovery", "ok")
	metrics.SetAuthTrustedDevices(1)
	metrics.SetAuthPendingChallenges(1)
	metrics.SetAuthBlockedBuckets(1)
}

// TestMetricsAuthExpositionCarriesNoIdentity proves no account, provider, or
// challenge identifier reaches the scrape output.
func TestMetricsAuthExpositionCarriesNoIdentity(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	metrics.AuthLogin("password", "failure")
	metrics.AuthMFAVerify("totp", "success")
	metrics.AuthOIDCStep("provision", "ok")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var exposition strings.Builder
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "tunnelmesh_auth_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				fmt.Fprintf(&exposition, "%s=%s ", label.GetName(), label.GetValue())
			}
		}
	}
	// Only label names and values are inspected: the family names legitimately
	// describe the concept they count, such as pending challenges.
	text := exposition.String()
	for _, forbidden := range []string{"user", "username", "provider", "challenge", "ticket", "device_id", "email", "secret", "token_id"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("auth metric exposition contains forbidden label %q:\n%s", forbidden, text)
		}
	}
}
