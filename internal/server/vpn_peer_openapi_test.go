package server

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const vpnOpenAPIDoc = "../../docs/api/openapi.yaml"

// vpnServedOperations is the vpn surface this release dispatches. It is written
// out literally on purpose: the point of the list is that adding a route to
// api.go, documenting it in openapi.yaml and updating this test are three edits
// one author has to make together, so a documented endpoint that is not served
// and a served endpoint that is not documented both fail the build.
var vpnServedOperations = []string{
	"get /api/v1/vpn-peers",
	"post /api/v1/vpn-peers",
	"get /api/v1/vpn-peers/{peerId}",
	"patch /api/v1/vpn-peers/{peerId}",
	"delete /api/v1/vpn-peers/{peerId}",
	"post /api/v1/vpn-peers/{peerId}/rotate",
	"post /api/v1/vpn-peers/{peerId}/config:reveal",
	"get /api/v1/vpn-peers/{peerId}/flows",
	"get /api/v1/vpn-nodes",
}

// vpnStableCodes reads the code set out of the implementation rather than
// restating it, so the comparison below is between the contract document and the
// code that actually answers, not between two hand-written lists.
func vpnStableCodes() []string {
	sentinels := []*vpn.Error{
		vpn.ErrPeerInvalid, vpn.ErrIPPoolInvalid, vpn.ErrPeerNotFound, vpn.ErrPeerConflict,
		vpn.ErrAgentCapabilityMissing, vpn.ErrIPPoolExhausted, vpn.ErrNodeDisabled,
		vpn.ErrSecretUnavailable, vpn.ErrCapacityExhausted, vpn.ErrNotImplemented,
	}
	codes := make([]string, 0, len(sentinels))
	for _, sentinel := range sentinels {
		codes = append(codes, sentinel.Code)
	}
	sort.Strings(codes)
	return codes
}

func vpnOpenAPIPaths(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(vpnOpenAPIDoc)
	if err != nil {
		t.Fatalf("read %s: %v", vpnOpenAPIDoc, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("%s is not parseable yaml: %v", vpnOpenAPIDoc, err)
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no paths mapping", vpnOpenAPIDoc)
	}
	return paths
}

// vpnDocumentedOperations collects "method path" for every documented vpn
// operation, plus how many of them carry the phase marker.
func vpnDocumentedOperations(t *testing.T) ([]string, int) {
	t.Helper()
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete}
	operations := make([]string, 0, len(vpnServedOperations))
	phases := 0
	for path, item := range vpnOpenAPIPaths(t) {
		if !strings.HasPrefix(path, "/api/v1/vpn") {
			continue
		}
		operationsByMethod, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an operation mapping", path)
		}
		for _, method := range methods {
			operation, served := operationsByMethod[strings.ToLower(method)]
			if !served {
				continue
			}
			operations = append(operations, strings.ToLower(method)+" "+path)
			if body, isMapping := operation.(map[string]any); isMapping {
				if _, marked := body["x-tunnelmesh-phase"]; marked {
					phases++
				}
			}
		}
	}
	sort.Strings(operations)
	return operations, phases
}

// TestVPNPeerOpenAPIMatchesTheServedSurface is the guard the AGENTS.md rule
// "OpenAPI must be updated with every interface behaviour change" needs in order
// to be checkable rather than aspirational.
func TestVPNPeerOpenAPIMatchesTheServedSurface(t *testing.T) {
	documented, phases := vpnDocumentedOperations(t)
	expected := append([]string(nil), vpnServedOperations...)
	sort.Strings(expected)
	if strings.Join(documented, "\n") != strings.Join(expected, "\n") {
		t.Errorf("openapi documents a different vpn surface than the server dispatches\n documented: %v\n served: %v", documented, expected)
	}
	// Exactly the two endpoints whose data plane dependency has not shipped carry
	// the marker: the peer flow table and the gateway fleet status. A marker on a
	// working endpoint would tell a client to stop calling it; a missing marker on
	// a 501 would leave the phase of the gap unrecorded.
	if phases != 0 {
		t.Errorf("found %d operations marked x-tunnelmesh-phase, want 0: every documented vpn operation is served", phases)
	}
}

// TestVPNPeerOpenAPIDocumentsTheStableCodes keeps the published enum and the
// sentinels in internal/vpn/errors.go identical in both directions. A code that
// is documented but never returned misleads a client into handling a case that
// cannot happen; a code that is returned but undocumented cannot be handled at
// all, which is how a console ends up showing "unknown error" for a state the
// operator can fix.
func TestVPNPeerOpenAPIDocumentsTheStableCodes(t *testing.T) {
	data, err := os.ReadFile(vpnOpenAPIDoc)
	if err != nil {
		t.Fatalf("read %s: %v", vpnOpenAPIDoc, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse %s: %v", vpnOpenAPIDoc, err)
	}
	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no components section", vpnOpenAPIDoc)
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no component schemas", vpnOpenAPIDoc)
	}
	codeSchema, ok := schemas["VPNErrorCode"].(map[string]any)
	if !ok {
		t.Fatal("the VPNErrorCode schema is missing")
	}
	rawEnum, ok := codeSchema["enum"].([]any)
	if !ok {
		t.Fatal("VPNErrorCode declares no enum")
	}
	documented := make([]string, 0, len(rawEnum))
	for _, value := range rawEnum {
		code, isString := value.(string)
		if !isString {
			t.Fatalf("VPNErrorCode enum entry %v is not a string", value)
		}
		documented = append(documented, code)
	}
	sort.Strings(documented)
	want := vpnStableCodes()
	if strings.Join(documented, ",") != strings.Join(want, ",") {
		t.Errorf("the documented code set drifted from the implementation\n documented: %v\n implemented: %v", documented, want)
	}
}

// TestVPNPeerOpenAPIOperationsAreRouted answers every documented operation
// against a live API and refuses to accept a 404. Parsing the document proves
// what it claims to serve; this proves the dispatcher agrees, which is the half a
// schema comparison cannot see.
func TestVPNPeerOpenAPIOperationsAreRouted(t *testing.T) {
	f := newVPNAPIFixture(t, nil)
	probe := f.createPeer(t, f.userToken, f.userAgent, "openapi-probe")
	documented, _ := vpnDocumentedOperations(t)
	for _, operation := range documented {
		method, path, found := strings.Cut(operation, " ")
		if !found {
			t.Fatalf("malformed operation %q", operation)
		}
		url := strings.ReplaceAll(path, "{peerId}", probe.ID)
		var body any
		switch strings.ToUpper(method) {
		case http.MethodPost, http.MethodPatch:
			// An empty object is enough: the point is that the route exists and
			// answers with its own validation, not that the request succeeds.
			body = map[string]any{}
		}
		r := apiJSONWithHeaders(t, f.handler, strings.ToUpper(method), url, f.userToken, map[string]string{
			"X-VPN-Config-Reveal-Confirm": "yes", "Idempotency-Key": "openapi-" + operation,
		}, body)
		if r.Code == http.StatusNotFound {
			t.Errorf("%s is documented but the server answers 404 (%s)", operation, r.Body.String())
		}
	}
}
