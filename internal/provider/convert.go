// Conversions between the two generators' idea of a value.
//
// terraform-plugin-framework models every attribute as a wrapper carrying a
// null/unknown state (`types.String`, `types.Int64`, `types.Number`). oapi-codegen
// models the same value as a Go field, and disagrees with the framework in five
// places at once:
//
//   - an `enum` becomes a *named* string type, and the name comes from the schema
//     the property was reached through, not from the operation — `method` is
//     `WebhookMethod` in the record and
//     `UpdateApplicationJSONBodyRecordAllCalls` in the update body;
//   - `format: uuid` becomes openapi_types.UUID, which Terraform has no attribute
//     type for;
//   - `type: number` becomes float32, where the framework has an
//     arbitrary-precision *big.Float;
//   - an optional or readOnly property becomes an `omitempty` pointer;
//   - `format: date-time` becomes time.Time, which Terraform has no attribute type
//     for either.
//
// So nothing in the generated code names an oapi-codegen type: every conversion
// goes through one of the helpers below, generic over the underlying type, and Go
// infers it from the field it is handed. A renamed enum type is then not a change
// to the generator at all, which is the property worth having.
//
// These are the only hand-written code the generated files depend on. They are
// hand-written because they are about Go's type system rather than about the API:
// nothing in the OpenAPI document decides any of it.
package provider

import (
	"math/big"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---- Terraform -> the wire -------------------------------------------------
//
// A null or unknown value leaves the destination alone. For a pointer field that
// means the property is omitted from the JSON body, which is what `omitempty` is
// for; for a value field it means the zero value, which is the only thing a
// non-omittable required property can be when the configuration has not said.

func setString[E ~string](dst *E, v types.String) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	*dst = E(v.ValueString())
}

func setStringPtr[E ~string](dst **E, v types.String) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	e := E(v.ValueString())
	*dst = &e
}

func setBool[E ~bool](dst *E, v types.Bool) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	*dst = E(v.ValueBool())
}

func setBoolPtr[E ~bool](dst **E, v types.Bool) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	e := E(v.ValueBool())
	*dst = &e
}

// integer is every width oapi-codegen picks for an integer property, and every
// named type it derives from one. Terraform has only Int64, so the conversion is
// always a narrowing the spec has already authorised by declaring the format.
type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

func setInt[E integer](dst *E, v types.Int64) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	*dst = E(v.ValueInt64())
}

func setIntPtr[E integer](dst **E, v types.Int64) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	e := E(v.ValueInt64())
	*dst = &e
}

type float interface {
	~float32 | ~float64
}

func setFloat[E float](dst *E, v types.Number) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	f, _ := v.ValueBigFloat().Float64()
	*dst = E(f)
}

func setFloatPtr[E float](dst **E, v types.Number) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	f, _ := v.ValueBigFloat().Float64()
	e := E(f)
	*dst = &e
}

// setUUID and setUUIDPtr parse rather than cast. A malformed uuid in configuration
// is silently dropped here rather than sent: the schema validates the format, and
// a parse error at this point has no diagnostic to attach itself to.
func setUUID(dst *openapi_types.UUID, v types.String) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	if u, err := uuid.Parse(v.ValueString()); err == nil {
		*dst = u
	}
}

func setUUIDPtr(dst **openapi_types.UUID, v types.String) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	if u, err := uuid.Parse(v.ValueString()); err == nil {
		*dst = &u
	}
}

// pathUUID renders a stored id as the openapi_types.UUID some of this API's path
// parameters declare. The id in state was minted by the server, so a parse
// failure means state has been corrupted by hand — and the zero uuid it yields
// addresses no record, so the read 404s and the resource is removed from state,
// which is the correct answer to a record Terraform can no longer identify.
func pathUUID(s string) openapi_types.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return openapi_types.UUID{}
	}
	return u
}

// ensure allocates a nested object through the pointer it is handed and returns
// it, so the generated code can assign the object's fields without naming its
// type. That is the whole point: the type of a webhook property is oapi-codegen's
// to choose.
func ensure[T any](dst **T) *T {
	if *dst == nil {
		*dst = new(T)
	}
	return *dst
}

// ---- the wire -> Terraform -------------------------------------------------

func int64PointerValue[E integer](v *E) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*v))
}

func floatValue[E float](v E) types.Number {
	return types.NumberValue(big.NewFloat(float64(v)))
}

func floatPointerValue[E float](v *E) types.Number {
	if v == nil {
		return types.NumberNull()
	}
	return types.NumberValue(big.NewFloat(float64(*v)))
}

func uuidValue(v openapi_types.UUID) types.String {
	return types.StringValue(v.String())
}

func uuidPointerValue(v *openapi_types.UUID) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(v.String())
}

// timeToString renders a timestamp as RFC 3339, which is what the API sent and
// what a practitioner comparing state against the API's own output expects to
// see.
func timeToString(v *time.Time) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(v.Format(time.RFC3339))
}
