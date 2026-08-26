// The wire half of the contract: what the generated Go client actually looks
// like, resolved from the same OpenAPI document oapi-codegen was handed.
//
// # The contract, in full
//
// A resource named `<r>` in generator_config.yml is bound to five operations,
// found in the spec **by operationId**:
//
//	create<R>   list<R>s   get<R>   update<R>   delete<R>
//
// The Grape layer cannot produce those names — grape-swagger mints
// `postApiV1Products` from the verb and the path — so
// codegen/tooling/schemas/terraform.overlay.yaml assigns them, and the overlay
// runner fails the build if any of its actions stops resolving. oapi-codegen then
// derives the Go method (`CreateProductWithResponse`) and the request body type
// (`CreateProductJSONRequestBody`) from the operationId, which is what makes both
// nameable from a template.
//
// Everything else is read, not assumed:
//
//   - Which properties each request body carries, and which are required. Create
//     and update do not agree in a Grape API: the create body requires the
//     client-minted ULID (FR-019a) and the PATCH body has no `id` at all. The
//     template used to send the create body's field set to both, which does not
//     compile the moment they differ.
//   - The success status per operation, from the lowest 2xx it declares. A Grape
//     `post` defaults to 201 and a transition that sets `status 200` does not, so
//     hard-coding either is wrong (docs/kernel.md).
//   - The response payload. Grape entities envelope — `{"product": {...}}` — so
//     the member named after the resource is the payload, and the glue reads
//     `.JSON201.Product`. A spec that returns the object directly is handled by
//     the same code path with an empty PayloadField.
//   - Whether each response field is a pointer, and whether it is a time.Time.
//     Both were inferred before: pointerness from "computed ⇔ pointer" and
//     timestamps from a `_at` name suffix. oapi-codegen decides pointerness from
//     `required` and `readOnly`, and time.Time from `format: date-time`, so those
//     are what get read.
//
// A resource whose operations cannot be resolved this way is a fatal error in
// cmd/gen. That is the whole point: the failure belongs here, next to the two
// files that disagree, and not in `go build` two stages downstream.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ---- the document ----------------------------------------------------------

// Document is the parts of an OpenAPI 3 document this command reads. Deliberately
// partial, like the Terraform IR above it: modelling more would mean maintaining
// a second OpenAPI library in a package whose go.mod is empty on purpose.
type Document struct {
	Paths      map[string]map[string]rawJSON `json:"paths"`
	Components struct {
		Schemas map[string]schemaObject `json:"schemas"`
	} `json:"components"`
}

// rawJSON is json.RawMessage under another name, so a path item's non-operation
// members (`parameters`, `summary`, `$ref`) can sit in the same map as its
// operations without the operation type having to accommodate them.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

type operation struct {
	OperationID string      `json:"operationId"`
	Parameters  []parameter `json:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema schemaObject `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema schemaObject `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

type parameter struct {
	Name   string       `json:"name"`
	In     string       `json:"in"`
	Schema schemaObject `json:"schema"`
}

type schemaObject struct {
	Ref        string                  `json:"$ref"`
	Type       string                  `json:"type"`
	Format     string                  `json:"format"`
	// Carried for the client manifest: an entity's `desc:` becomes a property
	// description here, and that is the only documentation a generated row type can
	// have. Dropping it would mean the field comments a device developer reads are the
	// one thing NOT derived from the entity.
	Description string                 `json:"description"`
	ReadOnly   bool                    `json:"readOnly"`
	// Immutable is `x-immutable`, the overlay's word for a property the API
	// accepts on create and refuses on update:
	//
	//	{"msg":"voip_carrier_sid may not be modified"}
	//
	// readOnly cannot say this — it would stop the create sending the property
	// too — and neither can the document's shape, because every jambonz update
	// is a whole-record PUT whose body $refs the record, so the update body
	// carries every property the record does. The alternative is giving each
	// such PUT an inline copy of the record schema minus a field or two, which
	// states the same fact at forty times the length and silently rots when the
	// record gains a property.
	//
	// An immutable property is in the create body and not the update body,
	// which is exactly Attribute.ForceNew — so saying this makes changing one
	// replace the record, rather than plan as an update the API rejects.
	Immutable  bool                    `json:"x-immutable"`
	// AllOf carries the one composition this document uses: `nullable: true` beside
	// an `allOf` of a single $ref, which is how OpenAPI 3.0 spells a nullable
	// reference — a $ref may not have siblings, so the reference is wrapped.
	AllOf      []schemaObject          `json:"allOf"`
	Required   []string                `json:"required"`
	Properties map[string]schemaObject `json:"properties"`
}

// methods are the path-item members that are operations. Everything else in a
// path item is metadata this command has no use for.
var methods = []string{"get", "put", "post", "patch", "delete"}

func readDocument(path string) Document {
	var doc Document
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v (run `make spec` — stage 3 writes it beside the YAML)", path, err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Paths) == 0 {
		log.Fatalf("%s declares no paths", path)
	}
	return doc
}

// resolve follows one level of `$ref` into components.schemas. One level is all
// an OpenAPI document produced by swagger2openapi ever needs, and refusing to
// recurse means a `$ref` cycle cannot hang the generator.
func (d Document) resolve(s schemaObject) (schemaObject, string, error) {
	// A nullable reference: `nullable: true` cannot sit beside a $ref, so the
	// reference is wrapped in a single-member allOf. Unwrap it and carry on as if
	// the property had been a plain $ref, which is what oapi-codegen does too.
	if s.Ref == "" && len(s.AllOf) == 1 {
		return d.resolve(s.AllOf[0])
	}
	// A wider allOf is a composition this generator does not model: the merged
	// property set would have to be computed here, and getting it subtly wrong
	// produces a body that compiles and omits a field.
	if len(s.AllOf) > 1 {
		return schemaObject{}, "", fmt.Errorf("allOf with %d members is not resolvable here; flatten it in the overlay", len(s.AllOf))
	}
	if s.Ref == "" {
		return s, "", nil
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(s.Ref, prefix) {
		return schemaObject{}, "", fmt.Errorf("unsupported $ref %q: only %s… is resolvable here", s.Ref, prefix)
	}
	name := strings.TrimPrefix(s.Ref, prefix)
	target, ok := d.Components.Schemas[name]
	if !ok {
		return schemaObject{}, "", fmt.Errorf("$ref %q names no schema in components", s.Ref)
	}
	if target.Ref != "" {
		return schemaObject{}, "", fmt.Errorf("schema %q is itself a $ref; refusing to chase it", name)
	}
	return target, name, nil
}

// operationByID indexes every operation in the document by its operationId. Built
// once per run, and it is where a duplicate id would show up — two operations
// with one id means oapi-codegen mints one method and drops the other.
func (d Document) operationByID() (map[string]boundOperation, error) {
	out := map[string]boundOperation{}
	for path, item := range d.Paths {
		// Parameters declared on the path item apply to every operation in it.
		// jambonz declares its {…Sid} that way — once per path, not once per
		// method — so an operation read without them looks like it addresses the
		// collection.
		var shared []parameter
		if raw, ok := item["parameters"]; ok {
			if err := json.Unmarshal(raw, &shared); err != nil {
				return nil, fmt.Errorf("%s: parameters: %w", path, err)
			}
		}
		for _, method := range methods {
			body, ok := item[method]
			if !ok {
				continue
			}
			var op operation
			if err := json.Unmarshal(body, &op); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			if op.OperationID == "" {
				continue
			}
			op.Parameters = append(op.Parameters, shared...)
			if prev, dup := out[op.OperationID]; dup {
				return nil, fmt.Errorf("operationId %q is on both %s and %s %s",
					op.OperationID, prev.Path, strings.ToUpper(method), path)
			}
			out[op.OperationID] = boundOperation{Path: path, Method: method, Op: op}
		}
	}
	return out, nil
}

type boundOperation struct {
	Path   string
	Method string
	Op     operation
}

// ---- what the templates see ------------------------------------------------

// Binding is one resource's whole wire contract.
type Binding struct {
	// IDAttr is the schema attribute that addresses one record, snake_cased from
	// the read operation's path parameter — `account_sid` for `{AccountSid}`.
	//
	// Not "id". Nothing in Terraform requires that name, tfplugingen-openapi
	// derives the attribute from the parameter, and this API names the parameter
	// after the record. Assuming "id" is how a generator ends up emitting
	// `data.Id` against a model that has no such field.
	IDAttr string

	// Payload is the member of each success response that holds the resource,
	// PascalCased — "Product" for a `{"product": {...}}` envelope. Empty when the
	// response is the object itself, and the templates then read the response
	// struct directly.
	Payload string
	// PayloadType is the oapi-codegen type of that member, for the one place a
	// type has to be named: the signature of the generated apply method.
	PayloadType string

	Create Op
	Read   Op
	Update Op
	Delete Op
	List   Op
}

// Op is one operation as the generated client presents it.
type Op struct {
	// Go is the client method name without the WithResponse suffix, which is the
	// operationId PascalCased — and, because the overlay assigns the operationId,
	// it is exactly `Create`+`Product`. Constructed rather than converted: there
	// is no case-conversion step to get subtly wrong, and the constructed name is
	// checked against the document.
	Go string
	// Success is the http status constant for the operation's success response.
	Success string
	// TakesID is set when the path carries the resource id, so the method takes it
	// positionally.
	TakesID bool
	// TakesParams is set when the operation has parameters outside the path, which
	// is what makes oapi-codegen add a *…Params argument. The glue passes nil: a
	// Terraform resource reads one record by id and a data source reads the
	// unfiltered collection (see generator_config.yml).
	TakesParams bool

	// IDParam is the path parameter's name as the spec spells it, for the message
	// when the schema turns out not to carry the attribute it implies.
	IDParam string

	// IDUUID is set when the path parameter declares `format: uuid`, which makes
	// oapi-codegen type the argument openapi_types.UUID rather than string. This
	// document does both — /Accounts/{AccountSid} declares the format and
	// /VoipCarriers/{VoipCarrierSid} does not — so which applies is per operation,
	// and reading it is the difference between compiling and not.
	IDUUID bool

	// ReadBack marks an operation that changes the record and does not answer with
	// it. Every jambonz write is one: create answers `{"sid": "…"}` and update
	// answers a bare 204. The glue then issues the read operation afterwards, which
	// is the only way state can reflect what the server stored — including the
	// columns it defaulted.
	ReadBack bool

	// Identifier is the field on a create acknowledgement that carries the new
	// record's id — `Sid` for `SuccessfulAdd`. Only set when ReadBack is, and it is
	// what the follow-up read is addressed by.
	Identifier string

	// JSON is the field oapi-codegen decodes the success body into — JSON201 for a
	// Grape `post`, JSON200 for a transition that sets its own status. Empty for an
	// operation with no body, which is what a 204 delete is.
	JSON string
	// Result is the expression the generated apply method is handed, envelope
	// already unwrapped: `&api.JSON201.Product`, or `api.JSON200` for an API that
	// answers with the record itself.
	Result string
}

// Args renders the arguments between ctx and the request body.
func (o Op) Args() string {
	var args []string
	if o.TakesID {
		if o.IDUUID {
			args = append(args, "pathUUID(id)")
		} else {
			args = append(args, "id")
		}
	}
	if o.TakesParams {
		args = append(args, "nil")
	}
	if len(args) == 0 {
		return ""
	}
	return ", " + strings.Join(args, ", ")
}

// ---- resolution ------------------------------------------------------------

// bind resolves the five canonical operations for one resource and annotates its
// attributes with the wire facts. Every failure is the contract being unmet, and
// every message names both halves of it.
func (d Document) bind(resource string, attrs []Attribute) (Binding, error) {
	ops, err := d.operationByID()
	if err != nil {
		return Binding{}, err
	}

	r := pascal(resource)
	// The plural is `+ "s"`, which is all the pluralisation this pipeline has ever
	// needed and all it should invent. A resource whose plural is irregular gets a
	// listing operationId that does not exist, and a named failure here.
	wanted := map[string]string{
		"create": "create" + r,
		"read":   "get" + r,
		"update": "update" + r,
		"delete": "delete" + r,
		"list":   "list" + r + "s",
	}

	found := map[string]boundOperation{}
	var missing []string
	for role, id := range wanted {
		op, ok := ops[id]
		if !ok {
			missing = append(missing, fmt.Sprintf("%s (%s)", id, role))
			continue
		}
		found[role] = op
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Binding{}, fmt.Errorf(
			"the spec declares no operation with operationId %s.\n"+
				"The resource is mapped in codegen/tooling/generator_config.yml and the canonical\n"+
				"operationIds are assigned in codegen/tooling/schemas/terraform.overlay.yaml. One of\n"+
				"the two has moved; they have to agree, because oapi-codegen names the client method\n"+
				"and the request body type from the operationId and this generator names them back.",
			strings.Join(missing, ", "))
	}

	binding := Binding{}
	for role, op := range found {
		built, err := d.buildOp(resource, wanted[role], op)
		if err != nil {
			return Binding{}, fmt.Errorf("%s operation %s: %w", role, wanted[role], err)
		}
		switch role {
		case "create":
			binding.Create = built
		case "read":
			binding.Read = built
		case "update":
			binding.Update = built
		case "delete":
			binding.Delete = built
		case "list":
			binding.List = built
		}
	}

	// Read, update and delete address one record, so their paths must carry the
	// id. Create and list address the collection, so they must not.
	for role, op := range map[string]Op{"read": binding.Read, "update": binding.Update, "delete": binding.Delete} {
		if !op.TakesID {
			return Binding{}, fmt.Errorf("%s operation %s has no {id} path parameter; it cannot address one record", role, op.Go)
		}
	}
	for role, op := range map[string]Op{"create": binding.Create, "list": binding.List} {
		if op.TakesID {
			return Binding{}, fmt.Errorf("%s operation %s takes a path parameter; it is meant to address the collection", role, op.Go)
		}
	}

	// The record is what a *read* answers with, and only that. Create and update
	// are then checked against it rather than merged with it — because on this API
	// neither returns it, and a generator that assumes they do binds `apply` to a
	// response field oapi-codegen never populates.
	payload, payloadType, payloadSchema, err := d.recordOf(resource, found["read"])
	if err != nil {
		return Binding{}, fmt.Errorf("read operation: %w", err)
	}
	binding.Payload, binding.PayloadType = payload, payloadType
	binding.Read.Result = resultExpr(binding.Read.JSON, payload)

	// The id attribute. Read addresses one record by a path parameter; the schema
	// has to carry that same value, because it is what create learns, what read is
	// addressed by, and what `terraform import` is handed.
	binding.IDAttr = snake(binding.Read.IDParam)

	// How each write answers. This is the fact that decides the entire shape of the
	// generated Create and Update, so it is resolved, not assumed.
	for _, w := range []struct {
		role string
		op   *Op
	}{{"create", &binding.Create}, {"update", &binding.Update}} {
		if err := d.bindWrite(w.op, resource, found[w.role], payload, payloadType); err != nil {
			return Binding{}, fmt.Errorf("%s operation %s: %w", w.role, w.op.Go, err)
		}
	}
	// A create that neither returns the record nor names the new record's id leaves
	// the glue with nothing to address the follow-up read by, and nothing to put in
	// state. There is no provider to generate from such an API.
	if binding.Create.ReadBack && binding.Create.Identifier == "" {
		return Binding{}, fmt.Errorf(
			"create operation %s answers with no body and no identifier.\n"+
				"Read, update, delete and `terraform import` all address the record by its id,\n"+
				"and this is the only moment the server could disclose it.", binding.Create.Go)
	}

	createBody, err := d.bodySchema(found["create"])
	if err != nil {
		return Binding{}, fmt.Errorf("create body: %w", err)
	}
	updateBody, err := d.bodySchema(found["update"])
	if err != nil {
		return Binding{}, fmt.Errorf("update body: %w", err)
	}

	if err := d.annotate(attrs, createBody, updateBody, payloadSchema, binding.IDAttr, true); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

// resultExpr is the expression the generated apply method is handed, envelope
// already unwrapped. Empty for an operation with no body.
func resultExpr(jsonField, payload string) string {
	switch {
	case jsonField == "":
		return ""
	case payload == "":
		return "api." + jsonField
	default:
		return "&api." + jsonField + "." + payload
	}
}

// bindWrite decides what one write operation's success response is: the record,
// an acknowledgement that names the record's id, or nothing at all.
//
// The first is the shape a generator naturally assumes and the one this API never
// uses. The other two both mean the same thing for the glue — the record has to be
// read back afterwards — and differ only in whether create learns the new id from
// a body or has nowhere to learn it from.
func (d Document) bindWrite(op *Op, resource string, bound boundOperation, payload, payloadType string) error {
	if op.JSON == "" {
		// A 204. The status alone says there is no body to decode.
		op.ReadBack = true
		return nil
	}

	member, typ, schema, err := d.recordOf(resource, bound)
	if err != nil {
		return err
	}
	if member == payload && typ == payloadType {
		op.Result = resultExpr(op.JSON, payload)
		return nil
	}

	// Not the record. An acknowledgement is a one-property object whose property is
	// the new record's identifier — jambonz answers `{"sid": "…"}`. Anything wider
	// is a third representation of the resource, and reconciling three is a decision
	// about the API rather than about the generator.
	if len(schema.Properties) != 1 {
		return fmt.Errorf(
			"answers with %s, but read answers with %s.\n"+
				"A write that does not return the record must return an acknowledgement naming\n"+
				"its id — a single property — so the record can be read back. This response has\n"+
				"%d properties. Narrow it in codegen/tooling/schemas/terraform.overlay.yaml.",
			describePayload(member, typ), describePayload(payload, payloadType), len(schema.Properties))
	}
	for name := range schema.Properties {
		op.Identifier = pascal(name)
	}
	op.ReadBack = true
	return nil
}

// recordOf resolves one operation's success body to the record it carries,
// unwrapping a same-named envelope if there is one.
func (d Document) recordOf(resource string, bound boundOperation) (string, string, schemaObject, error) {
	body, err := d.successSchema(bound.Op)
	if err != nil {
		return "", "", schemaObject{}, err
	}
	envelope, envelopeName, err := d.resolve(body)
	if err != nil {
		return "", "", schemaObject{}, err
	}
	// The envelope convention: an API that presents its record under a key named
	// after the record — `{"product": {...}}` — is unwrapped here so nothing
	// downstream has to reason about it. A response with no such property is taken
	// as the record itself, which is what jambonz sends.
	if prop, ok := envelope.Properties[resource]; ok {
		inner, innerName, err := d.resolve(prop)
		if err != nil {
			return "", "", schemaObject{}, fmt.Errorf("envelope member %q: %w", resource, err)
		}
		if innerName == "" {
			return "", "", schemaObject{}, fmt.Errorf(
				"envelope member %q is an inline schema; oapi-codegen gives it no nameable type", resource)
		}
		return pascal(resource), innerName, inner, nil
	}
	if envelopeName == "" {
		return "", "", schemaObject{}, fmt.Errorf(
			"the success body is an inline schema; oapi-codegen gives it no nameable type, and the\n" +
				"generated apply method has to name it. Move it into components.schemas.")
	}
	return "", envelopeName, envelope, nil
}

// bindRead is bind for a data source, which is one read operation and nothing
// else: no bodies to reconcile, no writes to classify, and no question about what
// the response is. The record it resolves is the same one the resource's binding
// resolves, from the same operation — which is what stops a lookup and a managed
// record disagreeing about how a field is decoded.
func (d Document) bindRead(name string, attrs []Attribute) (Binding, error) {
	ops, err := d.operationByID()
	if err != nil {
		return Binding{}, err
	}
	id := "get" + pascal(name)
	found, ok := ops[id]
	if !ok {
		return Binding{}, fmt.Errorf(
			"the spec declares no operation with operationId %q.\n"+
				"The data source is mapped in codegen/tooling/generator_config.yml and the canonical\n"+
				"operationIds are assigned in codegen/tooling/schemas/terraform.overlay.yaml.", id)
	}

	read, err := d.buildOp(name, id, found)
	if err != nil {
		return Binding{}, fmt.Errorf("read operation %s: %w", id, err)
	}
	if !read.TakesID {
		return Binding{}, fmt.Errorf("read operation %s has no {id} path parameter; it cannot address one record", read.Go)
	}

	binding := Binding{Read: read}
	payload, payloadType, payloadSchema, err := d.recordOf(name, found)
	if err != nil {
		return Binding{}, fmt.Errorf("read operation %s: %w", read.Go, err)
	}
	binding.Payload, binding.PayloadType = payload, payloadType
	binding.Read.Result = resultExpr(read.JSON, payload)
	binding.IDAttr = snake(read.IDParam)

	// Both request bodies are empty, so nothing is InCreate or InUpdate and every
	// attribute has to come from the record. An attribute that does not is one the
	// data source could never populate.
	if err := d.annotate(attrs, schemaObject{}, schemaObject{}, payloadSchema, binding.IDAttr, false); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

// buildOp turns one located operation into what the templates need.
func (d Document) buildOp(resource, id string, op boundOperation) (Op, error) {
	out := Op{Go: pascalOperationID(id)}

	for _, p := range op.Op.Parameters {
		if p.In == "path" {
			out.TakesID = true
			out.IDParam = p.Name
			out.IDUUID = isUUID(p.Schema)
			continue
		}
		out.TakesParams = true
	}
	// A path with more than one path parameter is a nested resource, which this
	// generator does not model — the templates pass exactly one id.
	if strings.Count(op.Path, "{") > 1 {
		return Op{}, fmt.Errorf("path %s has more than one path parameter; nested resources are not generated", op.Path)
	}

	status, err := successStatus(op.Op)
	if err != nil {
		return Op{}, err
	}
	out.Success = status
	out.JSON = jsonField(status)
	return out, nil
}

// successStatus is the lowest 2xx the operation declares, as a Go http constant.
// Reading it rather than assuming it is what a Grape API forces: `post` answers
// 201 by default and a transition that sets `status 200` answers 200
// (docs/kernel.md), and nothing but the spec knows which happened.
func successStatus(op operation) (string, error) {
	var codes []int
	for key := range op.Responses {
		code, err := strconv.Atoi(key)
		if err != nil {
			continue // "default", or a range like "2XX"
		}
		if code >= 200 && code < 300 {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return "", fmt.Errorf("declares no 2xx response")
	}
	sort.Ints(codes)
	if name, ok := statusConstants[codes[0]]; ok {
		return name, nil
	}
	return "", fmt.Errorf("success status %d has no net/http constant in this generator; add it", codes[0])
}

// statusConstants are the success statuses a CRUD endpoint can answer with. Named
// constants rather than integer literals, because the generated code is read.
var statusConstants = map[int]string{
	200: "http.StatusOK",
	201: "http.StatusCreated",
	202: "http.StatusAccepted",
	204: "http.StatusNoContent",
}

// jsonFields maps the same statuses onto the field oapi-codegen decodes that
// response into.
var jsonFields = map[string]string{
	"http.StatusOK":       "JSON200",
	"http.StatusCreated":  "JSON201",
	"http.StatusAccepted": "JSON202",
}

func jsonField(status string) string { return jsonFields[status] }

// pascalOperationID is the Go identifier oapi-codegen mints from an operationId.
// The canonical ids are lowerCamelCase with no separators, so upper-casing the
// first rune is the whole conversion — and anything else is a contract violation
// worth being loud about rather than silently transliterating.
func pascalOperationID(id string) string {
	if id == "" {
		return ""
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

func describePayload(member, typ string) string {
	if member == "" {
		return typ
	}
	return typ + " under ." + member
}

// successSchema is the JSON schema of the operation's lowest 2xx response.
func (d Document) successSchema(op operation) (schemaObject, error) {
	status, err := successStatus(op)
	if err != nil {
		return schemaObject{}, err
	}
	for code, response := range op.Responses {
		if name, ok := statusConstants[atoiOrZero(code)]; !ok || name != status {
			continue
		}
		content, ok := response.Content["application/json"]
		if !ok {
			return schemaObject{}, fmt.Errorf("%s response has no application/json body", status)
		}
		return content.Schema, nil
	}
	return schemaObject{}, fmt.Errorf("no %s response", status)
}

// bodySchema is the resolved application/json request body of an operation, or a
// zero schema when it has none. A zero schema annotates nothing, which is the
// right answer for an operation that sends no body.
func (d Document) bodySchema(op boundOperation) (schemaObject, error) {
	if op.Op.RequestBody == nil {
		return schemaObject{}, nil
	}
	content, ok := op.Op.RequestBody.Content["application/json"]
	if !ok {
		return schemaObject{}, fmt.Errorf("request body declares no application/json content")
	}
	resolved, _, err := d.resolve(content.Schema)
	return resolved, err
}

// annotate writes the wire facts onto the IR's attributes: which bodies carry
// each one, how oapi-codegen rendered it, and — for a nested object — the same
// again for every attribute inside it.
//
// An attribute the create body does not carry is not sent at create; the same for
// update. That difference is why the id is not sent in an update body that has no
// id property, and why an attribute that only create accepts is generated as
// force-new rather than as an update that silently does nothing.
// writable says whether the schema being annotated can be written at all. A data
// source has no request bodies, so its required attributes are lookup keys the
// practitioner supplies rather than properties something must send.
func (d Document) annotate(attrs []Attribute, createBody, updateBody, record schemaObject, idAttr string, writable bool) error {
	if err := d.annotateLevel(attrs, createBody, updateBody, record, writable); err != nil {
		return err
	}

	var haveID bool
	for i := range attrs {
		a := &attrs[i]
		if a.Name != idAttr {
			continue
		}
		if a.Type != "string" {
			return fmt.Errorf("the id attribute %q is a %s; a path parameter is a string", a.Name, a.Type)
		}
		if !a.InResponse {
			return fmt.Errorf(
				"the record has no %q property.\n"+
					"Read, update and delete address the record by it, and `terraform import` and\n"+
					"Crossplane's external name are both that string. An API whose record omits its\n"+
					"own identifier cannot be wrapped by Terraform at all.", a.Name)
		}
		haveID = true
	}
	if !haveID {
		return fmt.Errorf(
			"the read operation addresses one record by a path parameter, and the schema has no\n"+
				"%q attribute to carry it. tfplugingen-openapi derives that attribute from the\n"+
				"parameter, so this means the parameter and the record's own identity field\n"+
				"disagree — reconcile them in codegen/tooling/schemas/terraform.overlay.yaml.", idAttr)
	}
	return nil
}

// annotateLevel is annotate for one level of the schema, called again for each
// nested object against that object's own sub-schemas.
func (d Document) annotateLevel(attrs []Attribute, createBody, updateBody, record schemaObject, writable bool) error {
	createReq := setOf(createBody.Required)
	updateReq := setOf(updateBody.Required)
	recordReq := setOf(record.Required)

	for i := range attrs {
		a := &attrs[i]

		// A read-only property is never sent, whatever body schema declares it.
		// That is not a nicety here: every jambonz update is a whole-record PUT
		// whose body $refs the record — sid included — and the API rejects a body
		// that carries the sid.
		if prop, ok := createBody.Properties[a.Name]; ok && !prop.ReadOnly {
			a.InCreate = true
			a.CreateRequired = createReq[a.Name]
			a.CreateUUID = isUUID(prop)
		}
		if prop, ok := updateBody.Properties[a.Name]; ok && !prop.ReadOnly && !prop.Immutable {
			a.InUpdate = true
			a.UpdateRequired = updateReq[a.Name]
			a.UpdateUUID = isUUID(prop)
		}
		if prop, ok := record.Properties[a.Name]; ok {
			a.InResponse = true
			// oapi-codegen renders a property as an omitempty pointer when it is
			// optional, and readOnly makes it optional in every request body it
			// could appear in — so both produce a pointer field.
			a.ResponsePointer = !recordReq[a.Name] || prop.ReadOnly
			a.ResponseTime = prop.Type == "string" && prop.Format == "date-time"
			a.ResponseUUID = isUUID(prop)
		}

		// A Terraform-required attribute that no request body carries can never
		// be sent, which makes the resource unusable in a way `go build` would not
		// notice.
		if writable && a.Required() && !a.InCreate {
			return fmt.Errorf("attribute %q is required by the schema but is not a property of the create body", a.Name)
		}
		// Nor can one that neither body carries and no read populates: it would be
		// settable in configuration, never sent, and never read back — a diff on
		// every plan, forever.
		if !a.InCreate && !a.InUpdate && !a.InResponse {
			return fmt.Errorf(
				"attribute %q is in no request body and in no response.\n"+
					"Nothing can set it and nothing can read it. Either the spec is missing it\n"+
					"somewhere, or it should be ignored in codegen/tooling/generator_config.yml.", a.Name)
		}
		if err := a.checkKind(); err != nil {
			return err
		}

		if a.Type != "single_nested" {
			continue
		}
		// A nested object's attributes describe a different schema — the one the
		// property $refs — so they are annotated against it, and against it in each
		// of the three bodies independently. A webhook that is required in the
		// create body and optional in the record is exactly that case.
		nestedCreate, err := d.propertySchema(createBody, a.Name)
		if err != nil {
			return fmt.Errorf("attribute %q in the create body: %w", a.Name, err)
		}
		nestedUpdate, err := d.propertySchema(updateBody, a.Name)
		if err != nil {
			return fmt.Errorf("attribute %q in the update body: %w", a.Name, err)
		}
		nestedRecord, err := d.propertySchema(record, a.Name)
		if err != nil {
			return fmt.Errorf("attribute %q in the record: %w", a.Name, err)
		}
		if err := d.annotateLevel(a.Nested, nestedCreate, nestedUpdate, nestedRecord, writable); err != nil {
			return fmt.Errorf("attribute %q: %w", a.Name, err)
		}
	}
	return nil
}

// propertySchema resolves one property of a schema, following its $ref. A schema
// that does not carry the property yields the zero schema, which annotates
// nothing — the right answer for a body that omits it.
func (d Document) propertySchema(parent schemaObject, name string) (schemaObject, error) {
	prop, ok := parent.Properties[name]
	if !ok {
		return schemaObject{}, nil
	}
	resolved, _, err := d.resolve(prop)
	return resolved, err
}

// isUUID reports whether oapi-codegen rendered the property as
// openapi_types.UUID rather than a string. Terraform has no UUID attribute type,
// so every one of these needs converting in both directions — and the conversion
// cannot be inferred from the attribute, only from the property.
func isUUID(s schemaObject) bool { return s.Type == "string" && s.Format == "uuid" }

// snake converts a path parameter's name to the attribute name
// tfplugingen-openapi derives from it: AccountSid -> account_sid. The two have to
// agree, because one addresses the record and the other stores the same value.
func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// checkKind refuses the attribute kinds the templates have no conversion for.
// Failing here names the attribute; letting it through emits a call to a helper
// that does not exist.
func (a Attribute) checkKind() error {
	switch a.Type {
	case "string", "int64", "bool", "number", "single_nested":
		return nil
	default:
		return fmt.Errorf(
			"attribute %q is a %s, and the CRUD templates convert string, int64, bool, number\n"+
				"and single_nested only. Either ignore it in codegen/tooling/generator_config.yml,\n"+
				"or teach reqSet and respGet how to carry a %s.", a.Name, a.Type, a.Type)
	}
}

func setOf(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
