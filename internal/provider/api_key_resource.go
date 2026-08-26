// SPDX-License-Identifier: MPL-2.0

// jambonz_api_key: the one resource in this provider that is neither generated
// nor backed by the REST API.
//
// It cannot be either. The generator renders resources out of the OpenAPI
// description, and every call it renders authenticates with an API key — so the
// key that bootstraps a cluster is the one thing the API cannot give you. That
// is not an oversight in jambonz; its own installation instructions create the
// first key with an INSERT (db/create-admin-token.sql). This resource is that
// INSERT, expressed as state Terraform can own, and internal/jambonzdb is the
// only place the provider speaks SQL.
//
// Nothing else should follow it through this door. Every other kind of record
// has an API that validates the write; api_keys is reachable from the API only
// once you already hold a key.
package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-jambonz/internal/jambonzdb"
)

var (
	_ resource.Resource                = (*apiKeyResource)(nil)
	_ resource.ResourceWithImportState = (*apiKeyResource)(nil)
)

func NewApiKeyResource() resource.Resource { return &apiKeyResource{} }

type apiKeyResource struct {
	db *jambonzdb.Client
}

// ApiKeyModel is hand-written for the same reason the resource is: there is no
// OpenAPI schema to render it from.
type ApiKeyModel struct {
	Sid                types.String `tfsdk:"sid"`
	Token              types.String `tfsdk:"token"`
	AccountSid         types.String `tfsdk:"account_sid"`
	ServiceProviderSid types.String `tfsdk:"service_provider_sid"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A jambonz API key, written directly to the `api_keys` table of the jambonz database.\n\n" +
			"Every jambonz REST call authenticates with an API key, so the first key cannot be created through the " +
			"REST API. This resource needs the provider's `database` argument rather than `endpoint` and `api_key`, " +
			"and is the only resource in this provider that does.\n\n" +
			"The scope of a key is which sid is set: neither is an admin key, `service_provider_sid` scopes it to one " +
			"service provider, and `account_sid` to one account. Setting both is an error, and changing either " +
			"replaces the key — jambonz has no way to rescope one.",
		Attributes: map[string]schema.Attribute{
			"sid": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The `api_key_sid` assigned to the key. Also the import ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "The key itself, as passed to the jambonz API in the `Authorization: Bearer` " +
					"header. jambonz stores it in the clear, so unlike most providers' secrets this one survives " +
					"`terraform import` and can be re-read after a state loss.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"account_sid": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Scope the key to this account. Mutually exclusive with `service_provider_sid`.",
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("service_provider_sid")),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"service_provider_sid": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Scope the key to this service provider. Mutually exclusive with `account_sid`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the row was inserted, as the database recorded it.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	if data.DB == nil {
		resp.Diagnostics.AddError(
			"Jambonz database not configured",
			"jambonz_api_key is written directly to the jambonz database, because the REST API cannot mint the "+
				"key that would authenticate the call. Set the provider's `database` argument or the "+
				"JAMBONZ_DATABASE_URL environment variable.",
		)
		return
	}
	r.db = data.DB
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ApiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.db.CreateAPIKey(ctx, stringPtr(data.AccountSid), stringPtr(data.ServiceProviderSid))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the jambonz API key", err.Error())
		return
	}

	applyAPIKey(&data, key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ApiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.db.GetAPIKey(ctx, data.Sid.ValueString())
	if errors.Is(err, jambonzdb.ErrNotFound) {
		// Revoked out of band. Dropping it from state is what lets the next plan
		// mint a replacement rather than reporting a key that no longer works.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the jambonz API key", err.Error())
		return
	}

	applyAPIKey(&data, key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Update exists only because the interface demands it. Every attribute a
// practitioner can set requires replacement, so a plan never reaches this.
func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ApiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ApiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.db.DeleteAPIKey(ctx, data.Sid.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete the jambonz API key", err.Error())
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("sid"), req, resp)
}

// applyAPIKey writes a row back over the model. The scope columns are nullable,
// and a NULL is a null attribute rather than "" — the difference is admin scope
// versus an account whose sid is the empty string, which is not a thing.
func applyAPIKey(data *ApiKeyModel, key *jambonzdb.APIKey) {
	data.Sid = types.StringValue(key.Sid)
	data.Token = types.StringValue(key.Token)
	data.AccountSid = types.StringPointerValue(key.AccountSid)
	data.ServiceProviderSid = types.StringPointerValue(key.ServiceProviderSid)
	created := key.CreatedAt.UTC()
	data.CreatedAt = timeToString(&created)
}

// stringPtr renders an unset attribute as SQL NULL.
func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}
