// SPDX-License-Identifier: MPL-2.0

// The acceptance-test harness. Hand-written, like provider.go and for the same
// reason: how a test reaches a live server is a decision, not something the
// OpenAPI description implies.
//
// The generated <name>_resource_test.go files are the tests; everything here is
// what they have in common. They run against test/docker-compose.yml — a real
// api-server on the image the cluster runs, over a real MySQL — because the
// failures they cover only exist in the conversation between Terraform and a
// live server:
//
//   - "provider returned invalid result object after apply", which is Terraform
//     rejecting a value the provider left unknown. No unit test sees it: the
//     provider code is perfectly happy, and it is core that objects.
//
//   - an attribute that reads back differently from how it was written, which
//     needs a server that actually stores it.
//
//     docker compose -f test/docker-compose.yml up -d --wait
//     make testacc
package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"terraform-provider-jambonz/internal/api/jambonzapi"
)

// The seeded fixture data, from test/docker-compose.yml. The api-server's own
// migration creates the service provider and the account; test/seed.sql adds an
// admin key whose token is fixed, because the one the migration mints is
// randomly generated and a test cannot know it.
const (
	testAccEndpoint = "http://127.0.0.1:3000/v1"
	testAccAPIKey   = "38700987-c7a4-4685-a5bb-af378f9734de"

	// The database, for jambonz_api_key. Published on 13306 by
	// test/docker-compose.yml — see the comment there.
	testAccDatabaseURL = "mysql://root:jambones@127.0.0.1:13306/jambones"

	// Seeded by the api-server's own migration, and referred to by the
	// fixtures in schemas/acceptance.json.
	testAccAccountSid         = "9351f46a-678c-43f5-b8a6-d4eb58d131af"
	testAccServiceProviderSid = "2708b1b3-2736-40ea-b502-c53d8396247f"
)

// testAccProviderFactories is how the test harness gets a provider without a
// released binary: it serves the in-process implementation over the plugin
// protocol.
var testAccProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"jambonz": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck fails the test before Terraform runs rather than after, and
// says which of the two things is missing — the environment or the server.
//
// Failing rather than skipping on an unreachable server is deliberate. A skip
// is what you want for "this suite was not asked for", which TF_ACC already
// covers; once TF_ACC is set, a missing server is a broken run and silently
// passing an empty suite is how a regression gets shipped.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests skipped without TF_ACC; see test/docker-compose.yml")
	}

	// The provider reads these from the environment. Setting them here rather
	// than in every generated config keeps the fixtures to the resource under
	// test.
	t.Setenv("JAMBONZ_ENDPOINT", testAccEndpoint)
	t.Setenv("JAMBONZ_API_KEY", testAccAPIKey)
	// jambonz_api_key needs this one instead; the provider takes either or
	// both, and a resource says at Configure time which it needed.
	t.Setenv("JAMBONZ_DATABASE_URL", testAccDatabaseURL)

	client, err := testAccAPIClient()
	if err != nil {
		t.Fatal(err)
	}
	api, err := client.ListVoipCarriersWithResponse(t.Context())
	if err != nil {
		t.Fatalf("no api-server at %s: %v\n\nStart one with:\n\tdocker compose -f test/docker-compose.yml up -d --wait",
			testAccEndpoint, err)
	}
	if api.StatusCode() != http.StatusOK {
		t.Fatalf("api-server at %s answered %d to an authenticated list; is test/seed.sql applied?",
			testAccEndpoint, api.StatusCode())
	}
}

// testAccAPIClient is the client the generated CheckDestroy functions use to
// ask the server directly whether a record is really gone. Terraform's own
// state cannot answer that: it says what the provider reported, and a delete
// that reported success without deleting is precisely the bug.
func testAccAPIClient() (*jambonzapi.ClientWithResponses, error) {
	client, err := jambonzapi.NewClientWithResponses(testAccEndpoint,
		jambonzapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+testAccAPIKey)
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("build api client: %w", err)
	}
	return client, nil
}

// TestAccCoverage is the reason api_key stopped being the untested one.
//
// cmd/gen already refuses to generate a resource that has no fixture in
// schemas/acceptance.json, so nothing in the IR can go uncovered. api_key is
// not in the IR — it is hand-written, because it mints the key the API
// authenticates with — so that check could never see it, and it went untested
// precisely because it was the exception.
//
// This is the check for the exceptions: the provider's own registry, compared
// against the resources that have an acceptance test. Adding a resource of
// either kind and no test fails here, in an ordinary `go test`, without needing
// TF_ACC or a server.
func TestAccCoverage(t *testing.T) {
	// Every type name below is covered by a TestAcc* function in this package:
	// the generated <name>_resource_test.go files, and api_key_resource_test.go
	// by hand. Add the resource AND its test, then add it here.
	covered := map[string]bool{
		"jambonz_account":      true,
		"jambonz_api_key":      true,
		"jambonz_application":  true,
		"jambonz_phone_number": true,
		"jambonz_sip_gateway":  true,
		"jambonz_voip_carrier": true,
	}

	ctx := t.Context()
	p := New("test")()
	for _, newResource := range p.Resources(ctx) {
		var resp fwresource.MetadataResponse
		newResource().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "jambonz"}, &resp)
		if !covered[resp.TypeName] {
			t.Errorf("%s is registered by the provider and has no acceptance test.\n"+
				"Every resource gets one: generated from templates/resource_test.go.tmpl if it is in\n"+
				"the IR, hand-written like api_key_resource_test.go if it is not. Then add it to\n"+
				"`covered` here.", resp.TypeName)
		}
		delete(covered, resp.TypeName)
	}
	for name := range covered {
		t.Errorf("%s is listed as covered but the provider does not register it", name)
	}
}
