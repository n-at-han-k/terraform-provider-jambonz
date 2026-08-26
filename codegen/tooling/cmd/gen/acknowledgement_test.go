package main

import (
	"strings"
	"testing"
)

// The second wire shape, and the one this repository's API actually uses: a
// create that answers with an acknowledgement rather than the record, an update
// that answers 204 with no body at all, and a record addressed by a path
// parameter named after its own identity field.
//
// Every one of those was an assumption in the generator this was ported from —
// create returns the record, update returns the record, the id attribute is
// called "id" — and all three are false here. So they are tests: the happy path
// compiles either way, and a regression would only show up as generated code that
// reads a response field oapi-codegen never populates.
//
// The fixture mirrors /VoipCarriers, including its plain-string path parameter
// (some of the real paths declare `format: uuid` and some do not — see the uuid
// test below) and its nullable-$ref webhook.
const carrierSpec = `{
  "openapi": "3.1.0",
  "paths": {
    "/VoipCarriers": {
      "post": {
        "operationId": "createVoipCarrier",
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateBody"}}}},
        "responses": {
          "201": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/SuccessfulAdd"}}}},
          "422": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}
        }
      },
      "get": {
        "operationId": "listVoipCarriers",
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/VoipCarrier"}}}}}
      }
    },
    "/VoipCarriers/{VoipCarrierSid}": {
      "parameters": [{"name": "VoipCarrierSid", "in": "path", "schema": {"type": "string"}}],
      "get": {
        "operationId": "getVoipCarrier",
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/VoipCarrier"}}}}}
      },
      "put": {
        "operationId": "updateVoipCarrier",
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/VoipCarrier"}}}},
        "responses": {"204": {"description": "updated"}}
      },
      "delete": {
        "operationId": "deleteVoipCarrier",
        "responses": {"204": {"description": "deleted"}}
      }
    }
  },
  "components": {
    "schemas": {
      "Error": {"type": "object"},
      "SuccessfulAdd": {"type": "object", "required": ["sid"], "properties": {"sid": {"type": "string"}}},
      "CreateBody": {
        "type": "object",
        "required": ["name"],
        "properties": {
          "name": {"type": "string"},
          "account_sid": {"type": "string", "format": "uuid"},
          "tech_prefix": {"type": "string"}
        }
      },
      "VoipCarrier": {
        "type": "object",
        "required": ["voip_carrier_sid", "name"],
        "properties": {
          "voip_carrier_sid": {"type": "string", "format": "uuid", "readOnly": true},
          "name": {"type": "string"},
          "account_sid": {"type": "string", "format": "uuid"},
          "tech_prefix": {"type": "string"},
          "is_active": {"type": "boolean"}
        }
      }
    }
  }
}`

func carrierAttrs() []Attribute {
	return []Attribute{
		{Name: "voip_carrier_sid", Type: "string", ComputedOptionalRequired: "computed"},
		{Name: "name", Type: "string", ComputedOptionalRequired: "required"},
		{Name: "account_sid", Type: "string", ComputedOptionalRequired: "computed_optional"},
		{Name: "tech_prefix", Type: "string", ComputedOptionalRequired: "computed_optional"},
		{Name: "is_active", Type: "bool", ComputedOptionalRequired: "computed_optional"},
	}
}

func TestBindResolvesAWriteThenReadAPI(t *testing.T) {
	attrs := carrierAttrs()
	b, err := parse(t, carrierSpec).bind("voip_carrier", attrs)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	// Create answers with the acknowledgement, so the record has to be read back
	// and the new id comes from the acknowledgement's one property.
	if !b.Create.ReadBack {
		t.Error("create returns SuccessfulAdd, not the record; it has to be read back")
	}
	if b.Create.Identifier != "Sid" {
		t.Errorf("create identifier is %q, want %q — the follow-up read is addressed by it", b.Create.Identifier, "Sid")
	}
	if b.Create.Result != "" {
		t.Errorf("create has a payload expression %q; an acknowledgement is not the record", b.Create.Result)
	}

	// Update answers 204 with no body, so there is nothing to decode and the same
	// read has to follow.
	if !b.Update.ReadBack {
		t.Error("update answers 204 with no body; it has to be read back")
	}
	if b.Update.JSON != "" {
		t.Errorf("update decodes into %q; a 204 carries no body", b.Update.JSON)
	}
	if b.Update.Success != "http.StatusNoContent" {
		t.Errorf("update success status is %q, want http.StatusNoContent", b.Update.Success)
	}

	// Read is the only operation that answers with the record, so it is the only
	// one with a payload expression — and it is what apply's signature is typed on.
	if b.Read.Result != "api.JSON200" {
		t.Errorf("read payload expression is %q, want api.JSON200", b.Read.Result)
	}
	if b.PayloadType != "VoipCarrier" {
		t.Errorf("payload type is %q, want VoipCarrier", b.PayloadType)
	}
	if b.Payload != "" {
		t.Errorf("payload member is %q; this API answers with the record, not an envelope", b.Payload)
	}
}

func TestBindNamesTheIdAttributeAfterThePathParameter(t *testing.T) {
	attrs := carrierAttrs()
	b, err := parse(t, carrierSpec).bind("voip_carrier", attrs)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Nothing in Terraform requires the id attribute to be called "id", and
	// tfplugingen-openapi derives it from the parameter. Assuming "id" produces
	// glue that reads a model field that does not exist.
	if b.IDAttr != "voip_carrier_sid" {
		t.Errorf("id attribute is %q, want voip_carrier_sid (from {VoipCarrierSid})", b.IDAttr)
	}
}

func TestBindRejectsAnIdAttributeTheSchemaDoesNotCarry(t *testing.T) {
	// The path parameter and the record's identity field have to be the same value;
	// this is what it looks like when they are not.
	attrs := carrierAttrs()[1:] // drop voip_carrier_sid

	_, err := parse(t, carrierSpec).bind("voip_carrier", attrs)
	if err == nil {
		t.Fatal("bind accepted a schema with no attribute for the path parameter")
	}
	for _, want := range []string{"voip_carrier_sid", "terraform.overlay.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

func TestBindNeverSendsAReadOnlyProperty(t *testing.T) {
	attrs := carrierAttrs()
	if _, err := parse(t, carrierSpec).bind("voip_carrier", attrs); err != nil {
		t.Fatalf("bind: %v", err)
	}
	byName := map[string]Attribute{}
	for _, a := range attrs {
		byName[a.Name] = a
	}

	// The update body $refs the whole record, sid included, and this API rejects a
	// body that carries the sid. readOnly is how that is stated once rather than by
	// hand-copying the schema per operation.
	if sid := byName["voip_carrier_sid"]; sid.InCreate || sid.InUpdate {
		t.Error("the readOnly sid is sent in a request body; the API rejects that")
	}
	if !byName["voip_carrier_sid"].InResponse {
		t.Error("the sid is missing from the record")
	}

	// is_active is in the record and in the update body (which $refs the record),
	// and not in the create body. That difference is what ForceNew is read from —
	// and here it must NOT fire, because update does carry it.
	if a := byName["is_active"]; a.ForceNew() {
		t.Error("is_active is a property of the update body; changing it must not replace the record")
	}
	// name is in both, so it is an ordinary in-place update.
	if a := byName["name"]; a.ForceNew() {
		t.Error("name is a property of both bodies; changing it must not replace the record")
	}
}

func TestBindReadsForceNewFromTheBodyDifference(t *testing.T) {
	// A property the create body accepts and the update body does not: changing it
	// would plan as an in-place update that sends a body with no such property, the
	// API would store nothing, and the next read would show the old value back.
	narrowed := strings.Replace(carrierSpec,
		`"requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/VoipCarrier"}}}},
        "responses": {"204": {"description": "updated"}}`,
		`"requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}},
        "responses": {"204": {"description": "updated"}}`, 1)
	if narrowed == carrierSpec {
		t.Fatal("fixture edit did not apply; the assertion below would prove nothing")
	}

	attrs := carrierAttrs()
	if _, err := parse(t, narrowed).bind("voip_carrier", attrs); err != nil {
		t.Fatalf("bind: %v", err)
	}
	for _, a := range attrs {
		if a.Name != "tech_prefix" {
			continue
		}
		if !a.ForceNew() {
			t.Error("tech_prefix is in the create body and not the update body; it has to force replacement")
		}
	}
}

func TestBindReadsThePathParameterFormat(t *testing.T) {
	// Some of this API's path parameters declare `format: uuid` and some do not,
	// and oapi-codegen types the argument accordingly. Guessing either way is a
	// compile error two stages downstream.
	if b, err := parse(t, carrierSpec).bind("voip_carrier", carrierAttrs()); err != nil {
		t.Fatalf("bind: %v", err)
	} else if got := b.Read.Args(); got != ", id" {
		t.Errorf("read args are %q, want %q — the parameter is a plain string", got, ", id")
	}

	typed := strings.Replace(carrierSpec,
		`{"name": "VoipCarrierSid", "in": "path", "schema": {"type": "string"}}`,
		`{"name": "VoipCarrierSid", "in": "path", "schema": {"type": "string", "format": "uuid"}}`, 1)
	if typed == carrierSpec {
		t.Fatal("fixture edit did not apply")
	}
	b, err := parse(t, typed).bind("voip_carrier", carrierAttrs())
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got := b.Read.Args(); got != ", pathUUID(id)" {
		t.Errorf("read args are %q, want %q — the parameter declares format: uuid", got, ", pathUUID(id)")
	}
}

func TestBindResolvesPathItemLevelParameters(t *testing.T) {
	// The fixture declares {VoipCarrierSid} once on the path item, which is what
	// this API does throughout. An operation read without its path item's
	// parameters looks like it addresses the collection.
	b, err := parse(t, carrierSpec).bind("voip_carrier", carrierAttrs())
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	for name, op := range map[string]Op{"read": b.Read, "update": b.Update, "delete": b.Delete} {
		if !op.TakesID {
			t.Errorf("%s does not take the path parameter declared on its path item", name)
		}
	}
	if b.Create.TakesID {
		t.Error("create addresses the collection and must not take a path parameter")
	}
}

func TestBindRejectsACreateThatDisclosesNoIdentifier(t *testing.T) {
	// A create that answers 204 leaves the glue with no id to address the follow-up
	// read by, and nothing to put in state. There is no provider to generate.
	silent := strings.Replace(carrierSpec,
		`"201": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/SuccessfulAdd"}}}}`,
		`"204": {"description": "created"}`, 1)
	if silent == carrierSpec {
		t.Fatal("fixture edit did not apply")
	}

	_, err := parse(t, silent).bind("voip_carrier", carrierAttrs())
	if err == nil {
		t.Fatal("bind accepted a create that discloses no identifier")
	}
	if !strings.Contains(err.Error(), "no identifier") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveFollowsANullableRef(t *testing.T) {
	// `nullable: true` cannot sit beside a $ref, so OpenAPI 3.0 wraps the reference
	// in a single-member allOf — which is how the Account record declares its
	// registration webhook. Not following it makes every field of that webhook look
	// like it is in no response at all.
	doc := parse(t, carrierSpec)
	wrapped := schemaObject{AllOf: []schemaObject{{Ref: "#/components/schemas/VoipCarrier"}}}

	resolved, name, err := doc.resolve(wrapped)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if name != "VoipCarrier" {
		t.Errorf("resolved schema is named %q, want VoipCarrier", name)
	}
	if _, ok := resolved.Properties["name"]; !ok {
		t.Error("the resolved schema has no properties; the allOf was not followed")
	}

	// A wider allOf is a composition this generator does not model, and merging it
	// wrongly would produce a body that compiles and omits a field.
	if _, _, err := doc.resolve(schemaObject{AllOf: []schemaObject{{}, {}}}); err == nil {
		t.Error("resolve accepted a two-member allOf")
	}
}
