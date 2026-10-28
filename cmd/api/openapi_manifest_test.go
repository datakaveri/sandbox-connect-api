package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
)

// The gateway loads openapi/openapi.yaml as this service's operation manifest
// and DENIES any operation it does not declare. These tests hold the manifest
// and the router together in both directions, through the same compiler the
// gateway uses (manifest.FromOpenAPI), so drift fails here rather than as a
// 404 in an environment.

const manifestPath = "../../openapi/openapi.yaml"

// routesServedOutsideAPIMux are registered on the root mux in router().
var routesServedOutsideAPIMux = []string{
	"GET /v1/health",
	"GET /v1/apis",
	"GET /v1/apis/swagger.json",
	"GET /v1/apis/swagger.yaml",
	"GET /v1/apis/openapi.yaml",
}

func loadManifest(t *testing.T) (*manifest.Manifest, *manifest.Compiled) {
	t.Helper()
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(raw)
	if err != nil {
		t.Fatalf("the gateway could not load the manifest: %v", err)
	}
	m, err := manifest.FromOpenAPI(doc, manifest.CompileOptions{Service: "sandbox-connect-api"})
	if err != nil {
		t.Fatalf("the gateway would refuse to boot with this manifest: %v", err)
	}
	c, err := manifest.Compile(m)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return m, c
}

// registeredOperations is every method-qualified route across both booking
// modes, plus the root-mux routes.
func registeredOperations() map[string]bool {
	ops := map[string]bool{}
	for _, enabled := range []bool{true, false} {
		app := &application{env: ApiEnv{BookingsEnabled: enabled}}
		for _, rt := range app.apiRoutes() {
			if strings.Contains(rt.pattern, " ") {
				ops[rt.pattern] = true
			}
		}
	}
	for _, p := range routesServedOutsideAPIMux {
		ops[p] = true
	}
	return ops
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

func TestOperationManifestCompilesForTheGateway(t *testing.T) {
	m, _ := loadManifest(t)
	if len(m.Operations) == 0 {
		t.Fatal("manifest declares no operations")
	}
}

func TestEveryRegisteredRouteIsDeclaredInTheManifest(t *testing.T) {
	_, c := loadManifest(t)
	for op := range registeredOperations() {
		method, path, _ := strings.Cut(op, " ")
		concrete := pathParam.ReplaceAllString(path, "x1")
		if _, _, err := c.Match(method, concrete); err != nil {
			t.Errorf("%s is routed but not declared in openapi/openapi.yaml — the gateway would deny it (%v). "+
				"Add @ID and @x-dx-authz to the handler and run scripts/generate-openapi.sh", op, err)
		}
	}
}

func TestEveryDeclaredOperationIsRouted(t *testing.T) {
	m, _ := loadManifest(t)
	routed := registeredOperations()
	var stale []string
	for _, op := range m.Operations {
		key := op.Method + " " + op.Path
		if !routed[key] {
			stale = append(stale, key+" ("+op.OperationID+")")
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("declared but not routed in either booking mode:\n  %s", strings.Join(stale, "\n  "))
	}
}

// TestManifestPolicy pins the decisions that matter: what is public, and that
// nothing else is.
func TestManifestPolicy(t *testing.T) {
	m, _ := loadManifest(t)
	public := map[string]bool{
		"healthCheck":                       true,
		"getApiDocs":                        true,
		"getSwaggerJSON":                    true,
		"getSwaggerYAML":                    true,
		"getOpenAPIManifest":                true,
		"rotateNotebookTokenSession":        true, // service-authenticated: platform notebook token
		"rotateBookingNotebookTokenSession": true, // service-authenticated: platform notebook token
	}
	for _, op := range m.Operations {
		if public[op.OperationID] {
			if op.Authentication != manifest.AuthNone {
				t.Errorf("%s: want authentication none, got %q", op.OperationID, op.Authentication)
			}
			continue
		}
		if op.Authentication != manifest.AuthRequired || op.EffectiveAuthorization() != manifest.AuthzIdentity {
			t.Errorf("%s (%s %s): want required/identity, got %q/%q — every user operation is scoped to the caller inside the service",
				op.OperationID, op.Method, op.Path, op.Authentication, op.EffectiveAuthorization())
		}
	}
}
