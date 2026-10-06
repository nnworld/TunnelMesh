package server

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const clientScopedOpenAPIDoc = "../../docs/api/openapi.yaml"

// clientScopedServedOperations is the client-token surface this release dispatches,
// written out literally on purpose. Adding a route to handleClientScoped, documenting it
// in openapi.yaml and updating this list are three edits one author has to make together,
// so a documented endpoint that is not served and a served endpoint that is not
// documented both fail the build. The same guard already covers the vpn surface.
var clientScopedServedOperations = []string{
	"get /api/v1/client/agents",
}

// clientScopedDocumentedResponses are the statuses the namespace answers with. 503 is in
// the list because a Server started without a token validator is a wiring fault and must
// not be reported as an authentication failure.
var clientScopedDocumentedResponses = []string{"200", "401", "405", "503"}

func clientScopedOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(clientScopedOpenAPIDoc)
	if err != nil {
		t.Fatalf("read %s: %v", clientScopedOpenAPIDoc, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("%s is not parseable yaml: %v", clientScopedOpenAPIDoc, err)
	}
	return document
}

func clientScopedPaths(t *testing.T) map[string]any {
	t.Helper()
	paths, ok := clientScopedOpenAPI(t)["paths"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no paths mapping", clientScopedOpenAPIDoc)
	}
	return paths
}

// TestClientScopedOpenAPIMatchesTheServedSurface keeps the contract document and the
// dispatcher in step, which is what makes the AGENTS.md rule "every interface behaviour
// change updates openapi.yaml" checkable rather than aspirational.
func TestClientScopedOpenAPIMatchesTheServedSurface(t *testing.T) {
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	var documented []string
	for path, item := range clientScopedPaths(t) {
		// The plural admin namespace is a different surface with different credentials;
		// matching it here would hide the very confusion the singular prefix exists to
		// prevent.
		if !strings.HasPrefix(path, "/api/v1/client/") {
			continue
		}
		operations, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an operation mapping", path)
		}
		for _, method := range methods {
			if _, served := operations[strings.ToLower(method)]; served {
				documented = append(documented, strings.ToLower(method)+" "+path)
			}
		}
	}
	sort.Strings(documented)
	expected := append([]string(nil), clientScopedServedOperations...)
	sort.Strings(expected)
	if strings.Join(documented, "\n") != strings.Join(expected, "\n") {
		t.Errorf("openapi documents a different client-scoped surface than the server dispatches\n documented: %v\n served: %v", documented, expected)
	}
}

// TestClientScopedAgentsOperationDocumentsItsCredential pins the two facts that make this
// endpoint safe to expose at all: it authenticates with a client service token, and it
// answers a fixed set of statuses.
func TestClientScopedAgentsOperationDocumentsItsCredential(t *testing.T) {
	item, ok := clientScopedPaths(t)["/api/v1/client/agents"].(map[string]any)
	if !ok {
		t.Fatal("/api/v1/client/agents is not documented as an operation mapping")
	}
	operation, ok := item["get"].(map[string]any)
	if !ok {
		t.Fatal("/api/v1/client/agents does not document a GET operation")
	}

	encoded, err := yaml.Marshal(operation["security"])
	if err != nil {
		t.Fatalf("marshal the documented security requirement: %v", err)
	}
	security := string(encoded)
	if !strings.Contains(security, "clientTokenAuth") {
		t.Errorf("GET /api/v1/client/agents must require clientTokenAuth, documented as %s", strings.TrimSpace(security))
	}
	// A console bearer here would document that a management credential can enumerate the
	// agent fleet through the tunnel namespace, which is not what the handler does.
	if strings.Contains(security, "bearerAuth:") {
		t.Errorf("GET /api/v1/client/agents must not accept bearerAuth, documented as %s", strings.TrimSpace(security))
	}

	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		t.Fatal("/api/v1/client/agents documents no responses")
	}
	var documented []string
	for status := range responses {
		documented = append(documented, status)
	}
	sort.Strings(documented)
	expected := append([]string(nil), clientScopedDocumentedResponses...)
	sort.Strings(expected)
	if strings.Join(documented, ",") != strings.Join(expected, ",") {
		t.Errorf("GET /api/v1/client/agents documents responses %v, want %v", documented, expected)
	}
}

// TestClientAgentViewMatchesTheDocumentedSchema guards the disclosure boundary in both
// directions.
//
// The response is deliberately three fields wide because a tunnel token is a widely
// deployed credential, and every extra field widens what a leaked one discloses. Comparing
// the schema against the map clientAgentView actually emits means neither a new field in
// the handler nor a stale field in the document can slip through review unnoticed.
func TestClientAgentViewMatchesTheDocumentedSchema(t *testing.T) {
	encoded, err := json.Marshal(clientAgentView(storage.Agent{ID: "doc-agent", Name: "documented"}, true))
	if err != nil {
		t.Fatalf("marshal the served view: %v", err)
	}
	var served map[string]any
	if err := json.Unmarshal(encoded, &served); err != nil {
		t.Fatalf("unmarshal the served view: %v", err)
	}

	schemas, ok := clientScopedOpenAPI(t)["components"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no components mapping", clientScopedOpenAPIDoc)
	}
	definitions, ok := schemas["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no schemas mapping", clientScopedOpenAPIDoc)
	}
	schema, ok := definitions["ClientAgent"].(map[string]any)
	if !ok {
		t.Fatal("ClientAgent is not documented")
	}
	// additionalProperties: false is what makes the documented shape a constraint rather
	// than a suggestion. Read the value directly: the assertion is about the literal
	// false, and inverting it reads as a bug the first time someone writes it.
	if additional, present := schema["additionalProperties"]; !present {
		t.Error("ClientAgent must set additionalProperties: false, or the documented shape cannot constrain what the handler emits")
	} else if value, isBool := additional.(bool); !isBool || value {
		t.Errorf("ClientAgent sets additionalProperties: %v, want false", additional)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("ClientAgent documents no properties")
	}

	documented := make([]string, 0, len(properties))
	for name := range properties {
		documented = append(documented, name)
	}
	emitted := make([]string, 0, len(served))
	for name := range served {
		emitted = append(emitted, name)
	}
	sort.Strings(documented)
	sort.Strings(emitted)
	if strings.Join(documented, ",") != strings.Join(emitted, ",") {
		t.Errorf("ClientAgent documents %v but the handler emits %v", documented, emitted)
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("ClientAgent documents no required list")
	}
	if len(required) != len(emitted) {
		t.Errorf("ClientAgent requires %d fields but the handler emits %d; every emitted field is always present and must be required", len(required), len(emitted))
	}
}
