package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The contract in binding.go is what stops the pipeline emitting provider glue
// that calls client methods the client does not have. It has to fail here, loudly,
// with both halves of the disagreement named — which is exactly the property that
// only a test can hold onto, because the happy path compiles either way.
//
// The fixture is a miniature of the real thing: canonical operationIds assigned by
// the overlay, an enveloped response, and a create body that carries the
// client-minted id while the update body does not.
const productSpec = `{
  "openapi": "3.0.0",
  "paths": {
    "/api/v1/products": {
      "get": {
        "operationId": "listProducts",
        "parameters": [{"name": "q", "in": "query"}],
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/ProductList"}}}}}
      },
      "post": {
        "operationId": "createProduct",
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateBody"}}}},
        "responses": {
          "201": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/ProductEnvelope"}}}},
          "422": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}
        }
      }
    },
    "/api/v1/products/{id}": {
      "get": {
        "operationId": "getProduct",
        "parameters": [{"name": "id", "in": "path"}],
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/ProductEnvelope"}}}}}
      },
      "patch": {
        "operationId": "updateProduct",
        "parameters": [{"name": "id", "in": "path"}],
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/UpdateBody"}}}},
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/ProductEnvelope"}}}}}
      },
      "delete": {
        "operationId": "deleteProduct",
        "parameters": [{"name": "id", "in": "path"}],
        "responses": {"204": {}}
      }
    }
  },
  "components": {
    "schemas": {
      "Error": {"type": "object"},
      "ProductList": {"type": "object", "properties": {"products": {"type": "array"}}},
      "ProductEnvelope": {
        "type": "object",
        "properties": {"product": {"$ref": "#/components/schemas/ProductDetail"}},
        "required": ["product"]
      },
      "ProductDetail": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "name": {"type": "string"},
          "cost_cents": {"type": "integer", "format": "int32"},
          "company_id": {"type": "string", "readOnly": true},
          "created_at": {"type": "string", "format": "date-time", "readOnly": true}
        },
        "required": ["id", "name", "cost_cents", "company_id", "created_at"]
      },
      "CreateBody": {
        "type": "object",
        "properties": {"id": {"type": "string"}, "name": {"type": "string"}, "cost_cents": {"type": "integer"}},
        "required": ["id", "name"]
      },
      "UpdateBody": {
        "type": "object",
        "properties": {"name": {"type": "string"}, "cost_cents": {"type": "integer"}}
      }
    }
  }
}`

// attrs is the Terraform IR side of the fixture: what tfplugingen-openapi produces
// from the create body once the response envelope is ignored.
func attrs() []Attribute {
	return []Attribute{
		{Name: "id", Type: "string", ComputedOptionalRequired: "required"},
		{Name: "name", Type: "string", ComputedOptionalRequired: "required"},
		{Name: "cost_cents", Type: "int64", ComputedOptionalRequired: "computed_optional"},
	}
}

func parse(t *testing.T, spec string) Document {
	t.Helper()
	var doc Document
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	return doc
}

func TestBindResolvesTheCanonicalOperations(t *testing.T) {
	a := attrs()
	got, err := parse(t, productSpec).bind("product", a)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	for _, c := range []struct {
		what        string
		op          Op
		wantGo      string
		wantSuccess string
		wantJSON    string
		wantArgs    string
	}{
		{"create", got.Create, "CreateProduct", "http.StatusCreated", "JSON201", ""},
		{"read", got.Read, "GetProduct", "http.StatusOK", "JSON200", ", id"},
		{"update", got.Update, "UpdateProduct", "http.StatusOK", "JSON200", ", id"},
		{"delete", got.Delete, "DeleteProduct", "http.StatusNoContent", "", ", id"},
		{"list", got.List, "ListProducts", "http.StatusOK", "JSON200", ", nil"},
	} {
		if c.op.Go != c.wantGo {
			t.Errorf("%s method: want %s, got %s", c.what, c.wantGo, c.op.Go)
		}
		if c.op.Success != c.wantSuccess {
			t.Errorf("%s success: want %s, got %s", c.what, c.wantSuccess, c.op.Success)
		}
		if c.op.JSON != c.wantJSON {
			t.Errorf("%s JSON field: want %q, got %q", c.what, c.wantJSON, c.op.JSON)
		}
		if c.op.Args() != c.wantArgs {
			t.Errorf("%s args: want %q, got %q", c.what, c.wantArgs, c.op.Args())
		}
	}

	// The success status is read, not assumed: a Grape `post` answers 201 and a
	// transition that sets `status 200` answers 200, and only the spec knows which.
	if got.Create.Success == got.Read.Success {
		t.Error("create and read resolved the same success status; the 201 default was not read from the spec")
	}
}

func TestBindUnwrapsTheResponseEnvelope(t *testing.T) {
	got, err := parse(t, productSpec).bind("product", attrs())
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.Payload != "Product" {
		t.Errorf("payload member: want Product, got %q", got.Payload)
	}
	if got.PayloadType != "ProductDetail" {
		t.Errorf("payload type: want ProductDetail, got %q", got.PayloadType)
	}
	if want := "&api.JSON201.Product"; got.Create.Result != want {
		t.Errorf("create result: want %s, got %s", want, got.Create.Result)
	}
	if want := "&api.JSON200.Product"; got.Read.Result != want {
		t.Errorf("read result: want %s, got %s", want, got.Read.Result)
	}
}

// An API that answers with the record itself is handled by the same code path, and
// this is what says so — otherwise "unwrap the envelope" quietly becomes "require
// an envelope".
func TestBindHandlesAnUnenvelopedResponse(t *testing.T) {
	flat := strings.ReplaceAll(productSpec,
		`"$ref": "#/components/schemas/ProductEnvelope"`,
		`"$ref": "#/components/schemas/ProductDetail"`)

	got, err := parse(t, flat).bind("product", attrs())
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.Payload != "" {
		t.Errorf("payload member: want empty, got %q", got.Payload)
	}
	if want := "api.JSON201"; got.Create.Result != want {
		t.Errorf("create result: want %s, got %s", want, got.Create.Result)
	}
}

// The divergence that broke the old template: `id` is a property of the create
// body and not of the PATCH body, so sending the create field set to both emits a
// field that does not exist.
func TestBindSeparatesTheCreateAndUpdateBodies(t *testing.T) {
	a := attrs()
	if _, err := parse(t, productSpec).bind("product", a); err != nil {
		t.Fatalf("bind: %v", err)
	}

	byName := map[string]Attribute{}
	for _, attr := range a {
		byName[attr.Name] = attr
	}

	id := byName["id"]
	if !id.InCreate || !id.CreateRequired {
		t.Error("id is not a required property of the create body")
	}
	if id.InUpdate {
		t.Error("id is a property of the update body; it is addressed in the path")
	}

	cost := byName["cost_cents"]
	if !cost.InCreate || cost.CreateRequired {
		t.Error("cost_cents should be an optional create property")
	}
	if !cost.InUpdate || cost.UpdateRequired {
		t.Error("cost_cents should be an optional update property")
	}

	// And pointerness on the way back is read from `required` and `readOnly`, not
	// inferred from the attribute being computed.
	if byName["name"].ResponsePointer {
		t.Error("name is required and not readOnly, so the response field is a value")
	}
	if !byName["id"].InResponse {
		t.Error("id is missing from the response payload")
	}
}

func TestBindRejectsAMissingOperation(t *testing.T) {
	// The overlay assigned the operationId and generator_config.yml named the
	// resource; this is what a disagreement between the two looks like.
	renamed := strings.Replace(productSpec, `"operationId": "createProduct"`, `"operationId": "postApiV1Products"`, 1)

	_, err := parse(t, renamed).bind("product", attrs())
	if err == nil {
		t.Fatal("bind accepted a spec with no createProduct operation")
	}
	for _, want := range []string{"createProduct", "generator_config.yml", "terraform.overlay.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not say where to look:\n%v", want, err)
		}
	}
}

func TestBindRejectsDisagreeingPayloads(t *testing.T) {
	// One generated apply method is handed create's and update's responses as well
	// as read's, so a write that answers with something else has to be either the
	// record or an acknowledgement of it. A five-property list is neither.
	at := strings.Index(productSpec, `"getProduct"`)
	if at < 0 {
		t.Fatal("fixture has no getProduct operation")
	}
	mixed := productSpec[:at] + strings.Replace(productSpec[at:], "ProductEnvelope", "ProductList", 1)
	if mixed == productSpec {
		t.Fatal("fixture edit did not apply; the assertion below would prove nothing")
	}

	_, err := parse(t, mixed).bind("product", attrs())
	if err == nil {
		t.Fatal("bind accepted create, read and update returning different payloads")
	}
	for _, want := range []string{"ProductDetail under .Product", "ProductList", "acknowledgement"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not name the disagreement:\n%v", want, err)
		}
	}
}

func TestBindRejectsAnAttributeKindTheTemplatesCannotCarry(t *testing.T) {
	a := append(attrs(), Attribute{Name: "tags", Type: "list", ComputedOptionalRequired: "computed_optional"})

	_, err := parse(t, productSpec).bind("product", a)
	if err == nil {
		t.Fatal("bind accepted a list attribute the templates have no conversion for")
	}
	if !strings.Contains(err.Error(), "generator_config.yml") {
		t.Errorf("the error does not say how to resolve it:\n%v", err)
	}
}

func TestBindRequiresAnIdInTheResponse(t *testing.T) {
	// Read, update, delete and `terraform import` all address the record by its id.
	// An API whose success response omits its own identifier cannot be wrapped.
	noID := strings.Replace(productSpec, `"id": {"type": "string"},
          "name"`, `"name"`, 1)
	noID = strings.Replace(noID, `"required": ["id", "name", "cost_cents"`, `"required": ["name", "cost_cents"`, 1)
	if noID == productSpec {
		t.Fatal("fixture edit did not apply")
	}

	_, err := parse(t, noID).bind("product", attrs())
	if err == nil {
		t.Fatal(`bind accepted a response payload with no "id"`)
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBindRejectsADuplicateOperationID(t *testing.T) {
	// Two operations under one operationId means oapi-codegen mints one method and
	// drops the other, silently.
	dup := strings.Replace(productSpec, `"operationId": "listProducts"`, `"operationId": "getProduct"`, 1)

	_, err := parse(t, dup).bind("product", attrs())
	if err == nil {
		t.Fatal("bind accepted a duplicate operationId")
	}
	if !strings.Contains(err.Error(), "getProduct") {
		t.Errorf("the error does not name the duplicated id:\n%v", err)
	}
}
