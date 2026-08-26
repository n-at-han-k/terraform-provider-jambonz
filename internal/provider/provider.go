// SPDX-License-Identifier: MPL-2.0

// The provider itself: configuration, the API client, and the registry of what
// this provider manages.
//
// Hand-written, and one of only three files in this package that is — the
// others being convert.go and api_key_resource.go, the resource the OpenAPI
// description cannot describe. Everything
// else here is rendered by codegen/tooling/cmd/gen from the OpenAPI description —
// see the header of any *_resource.go. Nothing below is derivable from the spec:
// how a practitioner supplies an endpoint and a key is a provider design
// decision, and the registry is the one place a new resource has to be
// acknowledged by hand.
package provider

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-jambonz/internal/api/jambonzapi"
	"terraform-provider-jambonz/internal/jambonzdb"
)

var _ provider.Provider = &JambonzProvider{}

// JambonzProvider defines the provider implementation.
type JambonzProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

// JambonzProviderModel describes the provider data model.
type JambonzProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	ApiKey   types.String `tfsdk:"api_key"`
	Database types.String `tfsdk:"database"`
}

// providerData is what every resource and data source receives from Configure.
//
// There are two ways into jambonz and a configuration may hold either or both:
// the REST API, which is how everything is managed, and the database, which is
// how the first API key is minted because the REST API cannot mint the key that
// would authenticate the call. Either handle may be nil — a bootstrap run has
// no key yet, and an ordinary run has no reason to hand Terraform the database
// password — so each resource says at Configure time which one it needed.
type providerData struct {
	API *jambonzapi.ClientWithResponses
	DB  *jambonzdb.Client
}

func (p *JambonzProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "jambonz"
	resp.Version = p.version
}

func (p *JambonzProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Description: "Jambonz API endpoint, including the /v1 path. May also be provided via the JAMBONZ_ENDPOINT environment variable.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "Jambonz API key. May also be provided via the JAMBONZ_API_KEY environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"database": schema.StringAttribute{
				Description: "Connection string for the jambonz MySQL database, either as a go-sql-driver DSN " +
					"(\"user:pass@tcp(host:3306)/jambones\") or a URL (\"mysql://user:pass@host:3306/jambones\"). " +
					"Only `jambonz_api_key` uses it, and only because the REST API cannot create the key that would " +
					"authenticate the call. May also be provided via the JAMBONZ_DATABASE_URL environment variable.",
				Optional:  true,
				Sensitive: true,
			},
		},
	}
}

func (p *JambonzProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config JambonzProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown value here comes from another resource's attribute that has not
	// been applied yet. The client is built once, before any of that, so the only
	// answer is to say which value and why.
	if config.Endpoint.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Unknown Jambonz API endpoint",
			"The provider cannot create the Jambonz API client as there is an unknown configuration value for the Jambonz API endpoint. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the JAMBONZ_ENDPOINT environment variable.",
		)
	}
	if config.ApiKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Unknown Jambonz API key",
			"The provider cannot create the Jambonz API client as there is an unknown configuration value for the Jambonz API key. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the JAMBONZ_API_KEY environment variable.",
		)
	}
	if config.Database.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("database"),
			"Unknown Jambonz database connection string",
			"The provider cannot connect to the Jambonz database as there is an unknown configuration value for the connection string. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the JAMBONZ_DATABASE_URL environment variable.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := os.Getenv("JAMBONZ_ENDPOINT")
	apiKey := os.Getenv("JAMBONZ_API_KEY")
	database := os.Getenv("JAMBONZ_DATABASE_URL")
	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}
	if !config.ApiKey.IsNull() {
		apiKey = config.ApiKey.ValueString()
	}
	if !config.Database.IsNull() {
		database = config.Database.ValueString()
	}

	// A configuration may hold API credentials, a database connection string, or
	// both, and which of those it needs depends on the resources in it. The one
	// case that is always wrong is neither.
	//
	// Half an API configuration is also always wrong — an endpoint with no key,
	// or a key with no endpoint, is a mistake rather than a deliberate
	// database-only run, and saying so here is much clearer than the 401 it
	// would otherwise become.
	apiConfigured := endpoint != "" || apiKey != ""
	if endpoint == "" && (apiConfigured || database == "") {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing Jambonz API endpoint",
			"The provider cannot create the Jambonz API client as there is a missing or empty value for the Jambonz API endpoint. "+
				"Set it in the configuration or use the JAMBONZ_ENDPOINT environment variable.",
		)
	}
	if apiKey == "" && (apiConfigured || database == "") {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Missing Jambonz API key",
			"The provider cannot create the Jambonz API client as there is a missing or empty value for the Jambonz API key. "+
				"Set it in the configuration or use the JAMBONZ_API_KEY environment variable. "+
				"A configuration that only manages `jambonz_api_key` may instead set `database` alone, which is how the first key is created.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	data := &providerData{}

	if endpoint != "" && apiKey != "" {
		client, err := jambonzapi.NewClientWithResponses(
			strings.TrimSuffix(endpoint, "/"),
			jambonzapi.WithRequestEditorFn(bearer(apiKey)),
		)
		if err != nil {
			resp.Diagnostics.AddError(
				"Unable to create Jambonz API client",
				"An unexpected error occurred when creating the Jambonz API client. Jambonz client error: "+err.Error(),
			)
			return
		}
		data.API = client
	}

	if database != "" {
		db, err := jambonzdb.Open(database)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("database"),
				"Unable to connect to the Jambonz database",
				err.Error(),
			)
			return
		}
		data.DB = db
	}

	resp.DataSourceData = data
	resp.ResourceData = data
}

// bearer applies the spec's `bearerAuth` security scheme. oapi-codegen generates
// the securitySchemes into the client's provider interface but does not implement
// one, on purpose — how a token is obtained is not something an OpenAPI document
// can say.
func bearer(token string) jambonzapi.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// Resources and DataSources are the registry. Adding a resource is two edits: the
// operations it is made of, in codegen/tooling/generator_config.yml, and its
// constructor here. The constructor's name is `New<Resource>Resource`, generated
// from the same name.
func (p *JambonzProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAccountResource,
		NewApiKeyResource,
		NewApplicationResource,
		NewPhoneNumberResource,
		NewSipGatewayResource,
		NewVoipCarrierResource,
	}
}

func (p *JambonzProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewAccountDataSource,
		NewApplicationDataSource,
		NewPhoneNumberDataSource,
		NewSipGatewayDataSource,
		NewVoipCarrierDataSource,
	}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &JambonzProvider{
			version: version,
		}
	}
}
