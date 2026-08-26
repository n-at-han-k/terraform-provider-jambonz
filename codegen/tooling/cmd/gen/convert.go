// Conversions between the two generators' idea of a value.
//
// terraform-plugin-framework models every attribute as a wrapper carrying a
// null/unknown state (`types.String`, `types.Int64`, `types.Bool`). oapi-codegen
// models the same value as a Go field, and disagrees with the framework in four
// places at once:
//
//   - an `enum` becomes a *named* string type, and the name comes from the schema
//     the property was reached through, not from the operation — `unit` is
//     `PostApiV1ProductsUnit` in the create body, `PatchApiV1ProductsIdUnit` in
//     the update body and `ProductDetailUnit` in the response;
//   - `format: int32` becomes `int32` where Terraform has only `types.Int64`;
//   - an optional or readOnly property becomes an `omitempty` pointer;
//   - `format: date-time` becomes `time.Time`, which Terraform has no attribute
//     type for at all.
//
// The old template cast between them by *naming* oapi-codegen's types, guessing
// each one from the verb and the resource (`CreateProductUnit`). Those names do
// not exist. So nothing here names an oapi-codegen type: every conversion goes
// through a generic helper hand-written beside the generated code —
// `setStringPtr[E ~string](dst **E, v types.String)` — and Go infers E from the
// field it is handed. A renamed enum type is then not a change to this generator
// at all, which is the property worth having.
//
// The helpers live in internal/provider/convert.go. They are the only
// hand-written code the generated files depend on.
package main

import (
	"fmt"
	"log"
)

// setters name the helper per attribute kind, for a value field and a pointer
// field. Keyed by the IR's type tag; Attribute.checkKind has already refused
// anything not in here.
var setters = map[string][2]string{
	"string": {"setString", "setStringPtr"},
	"int64":  {"setInt", "setIntPtr"},
	"bool":   {"setBool", "setBoolPtr"},
	"number": {"setFloat", "setFloatPtr"},
	// Terraform models a uuid as a string; oapi-codegen models it as
	// openapi_types.UUID. Keyed separately from "string" because which of the two
	// applies is a property of the body, not of the attribute.
	"uuid": {"setUUID", "setUUIDPtr"},
}

// reqSet renders one assignment into a request body. verb is "Create" or
// "Update"; src is the Terraform model variable the value comes from.
//
// Pointerness is the *body's*, per operation — not the schema's. A property that
// the create body requires and the update body merely accepts is a value in one
// and a pointer in the other, and that is a fact about the spec rather than about
// the attribute.
func reqSet(a Attribute, verb, src string) string {
	return fmt.Sprintf("%s(&%s.%s, %s.%s)",
		setter(a, verb, !reqRequired(a, verb)), "body", pascal(a.Name), src, pascal(a.Name))
}

// reqSetField is reqSet for one field of a nested object: both sides are already
// addressed by the caller, so the names are passed in whole.
func reqSetField(a Attribute, verb, dst, src string) string {
	return fmt.Sprintf("%s(&%s, %s)", setter(a, verb, !reqRequired(a, verb)), dst, src)
}

func reqRequired(a Attribute, verb string) bool {
	if verb == "Update" {
		return a.UpdateRequired
	}
	return a.CreateRequired
}

func setter(a Attribute, verb string, pointer bool) string {
	kind := a.Type
	if (verb == "Update" && a.UpdateUUID) || (verb != "Update" && a.CreateUUID) {
		kind = "uuid"
	}
	names, ok := setters[kind]
	if !ok {
		log.Fatalf("attribute %q: no setter for a %s", a.Name, kind)
	}
	if pointer {
		return names[1]
	}
	return names[0]
}

// respGet renders the conversion from the response payload back onto the
// Terraform model. `p` is the payload value the generated apply method is handed.
//
// The string conversion is emitted unconditionally rather than only for enums:
// `string(x)` where x is already a string, and `(*string)(p)` where p is already
// a *string, are both legal no-op conversions in Go. So the cast is correct
// whether or not the property turned out to be a named enum type — and unlike the
// old code it does not have to *know* which, because it never spells the name.
func respGet(a Attribute) string { return respGetFrom(a, "p."+pascal(a.Name)) }

// respGetFrom is respGet against an already-addressed field, for the members of a
// nested object.
func respGetFrom(a Attribute, field string) string {
	switch {
	case a.ResponseUUID && a.ResponsePointer:
		return fmt.Sprintf("uuidPointerValue(%s)", field)
	case a.ResponseUUID:
		return fmt.Sprintf("uuidValue(%s)", field)

	case a.ResponseTime && a.ResponsePointer:
		return fmt.Sprintf("timeToString(%s)", field)
	case a.ResponseTime:
		return fmt.Sprintf("timeToString(&%s)", field)

	case a.Type == "string" && a.ResponsePointer:
		return fmt.Sprintf("types.StringPointerValue((*string)(%s))", field)
	case a.Type == "string":
		return fmt.Sprintf("types.StringValue(string(%s))", field)

	// int64PointerValue is generic over the integer width oapi-codegen chose, so
	// a `format: int32` property does not need converting before it is read.
	case a.Type == "int64" && a.ResponsePointer:
		return fmt.Sprintf("int64PointerValue(%s)", field)
	case a.Type == "int64":
		return fmt.Sprintf("types.Int64Value(int64(%s))", field)

	case a.Type == "bool" && a.ResponsePointer:
		return fmt.Sprintf("types.BoolPointerValue((*bool)(%s))", field)
	case a.Type == "bool":
		return fmt.Sprintf("types.BoolValue(bool(%s))", field)

	// types.Number is an arbitrary-precision *big.Float, so neither direction is a
	// cast; both go through a helper generic over the float width oapi-codegen
	// chose for the property.
	case a.Type == "number" && a.ResponsePointer:
		return fmt.Sprintf("floatPointerValue(%s)", field)
	case a.Type == "number":
		return fmt.Sprintf("floatValue(%s)", field)
	}
	log.Fatalf("attribute %q: no response conversion for a %s", a.Name, a.Type)
	return ""
}

// nestedDst is the destination a nested object's fields are assigned through.
//
// oapi-codegen renders a required object property as a value field and an
// optional one as a pointer, so the two need different handling — and `ensure`
// allocates through the pointer it is handed and infers the struct type, which is
// how the generated code avoids naming it.
func nestedDst(a Attribute, verb string) string {
	field := "body." + pascal(a.Name)
	if reqRequired(a, verb) {
		return "&" + field
	}
	return fmt.Sprintf("ensure(&%s)", field)
}

// nullValue is a typed null, for a nested attribute the record does not carry.
// The framework's object constructor checks every attribute type, so an untyped
// null is a runtime diagnostic rather than a missing field.
func nullValue(a Attribute) string {
	switch a.Type {
	case "string":
		return "types.StringNull()"
	case "int64":
		return "types.Int64Null()"
	case "bool":
		return "types.BoolNull()"
	case "number":
		return "types.NumberNull()"
	}
	log.Fatalf("attribute %q: no null value for a %s", a.Name, a.Type)
	return ""
}
