// SPDX-License-Identifier: MPL-2.0

// Hand-written, for the same reason api_key_resource.go is: cmd/gen renders a
// test out of the OpenAPI description, and this resource is the one thing the
// description cannot describe. The shape below deliberately follows the
// generated ones — create, import, destroy — so the odd one out reads like the
// rest.
//
// What is not shared with them is the last check. Every other resource is
// proved by reading it back through the API; this one mints the key the API
// authenticates with, so the proof is that the key WORKS: a REST call carrying
// a freshly created token, made with a client the provider knows nothing about.
// A row in api_keys that jambonz will not accept is a bootstrap that fails on
// the pass after it, which is the failure this resource exists to prevent.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-jambonz/internal/jambonzdb"
)

// An admin key: neither scope column set. This is the bootstrap case — the key
// jambonz's own install instructions create with an INSERT.
func TestAccApiKeyAdmin(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProviderFactories,
		CheckDestroy:             testAccCheckApiKeyDestroyed,
		Steps: []resource.TestStep{
			{
				Config: `resource "jambonz_api_key" "test" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("jambonz_api_key.test", "sid"),
					resource.TestCheckResourceAttrSet("jambonz_api_key.test", "token"),
					// created_at is a column default: the database knows it and
					// the provider does not until it reads the row back. If that
					// read were skipped it would still be unknown here, and
					// unknown-after-apply is an error rather than a null.
					resource.TestCheckResourceAttrSet("jambonz_api_key.test", "created_at"),
					// Admin scope is both columns NULL, and NULL has to arrive as
					// a null attribute rather than "" — an account whose sid is
					// the empty string is not a thing.
					resource.TestCheckNoResourceAttr("jambonz_api_key.test", "account_sid"),
					resource.TestCheckNoResourceAttr("jambonz_api_key.test", "service_provider_sid"),
					testAccCheckApiKeyAuthenticates("jambonz_api_key.test"),
				),
			},
			{
				ResourceName:                         "jambonz_api_key.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateIdFunc:                    testAccImportIDApiKey,
				ImportStateVerifyIdentifierAttribute: "sid",
			},
		},
	})
}

// A scoped key, and the replacement that rescoping forces. jambonz has no way
// to rescope a key, so account_sid is RequiresReplace; the second step proves
// the plan really does replace rather than quietly reporting success.
func TestAccApiKeyScoped(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProviderFactories,
		CheckDestroy:             testAccCheckApiKeyDestroyed,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "jambonz_api_key" "test" {
  account_sid = %q
}
`, testAccAccountSid),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("jambonz_api_key.test", "account_sid", testAccAccountSid),
					resource.TestCheckNoResourceAttr("jambonz_api_key.test", "service_provider_sid"),
					testAccCheckApiKeyAuthenticates("jambonz_api_key.test"),
				),
			},
			{
				Config: fmt.Sprintf(`
resource "jambonz_api_key" "test" {
  service_provider_sid = %q
}
`, testAccServiceProviderSid),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("jambonz_api_key.test", "service_provider_sid", testAccServiceProviderSid),
					resource.TestCheckNoResourceAttr("jambonz_api_key.test", "account_sid"),
				),
			},
		},
	})
}

// The two scopes are mutually exclusive, and the schema says so with a
// ConflictsWith validator. A validator that has never been exercised is a
// validator that might be attached to the wrong attribute.
func TestAccApiKeyScopesConflict(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "jambonz_api_key" "test" {
  account_sid          = %q
  service_provider_sid = %q
}
`, testAccAccountSid, testAccServiceProviderSid),
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
}

// testAccCheckApiKeyAuthenticates makes a real REST call with the minted token.
func testAccCheckApiKeyAuthenticates(name string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not in state", name)
		}
		token := rs.Primary.Attributes["token"]
		if token == "" {
			return fmt.Errorf("%s: no token in state", name)
		}

		req, err := http.NewRequest(http.MethodGet, testAccEndpoint+"/Accounts", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: the minted key does not authenticate — GET /Accounts answered %d",
				name, res.StatusCode)
		}
		return nil
	}
}

func testAccImportIDApiKey(s *terraform.State) (string, error) {
	rs, ok := s.RootModule().Resources["jambonz_api_key.test"]
	if !ok {
		return "", fmt.Errorf("jambonz_api_key.test not in state")
	}
	return rs.Primary.Attributes["sid"], nil
}

// Checked against the database rather than the API: a revoked key cannot ask
// the API whether it still exists.
func testAccCheckApiKeyDestroyed(s *terraform.State) error {
	db, err := jambonzdb.Open(testAccDatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	for name, rs := range s.RootModule().Resources {
		if rs.Type != "jambonz_api_key" {
			continue
		}
		sid := rs.Primary.Attributes["sid"]
		_, err := db.GetAPIKey(context.Background(), sid)
		if errors.Is(err, jambonzdb.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return fmt.Errorf("%s: destroyed, but api_keys still has a row for sid %s", name, sid)
	}
	return nil
}
