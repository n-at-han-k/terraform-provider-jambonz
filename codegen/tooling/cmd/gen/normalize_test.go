package main

import (
	"encoding/json"
	"testing"
)

// Stage 4a′ edits a generated artifact between two HashiCorp generators, which is
// a place a mistake is easy to miss: its output still looks like an IR, and the
// first sign of a wrong edit is a provider whose schema is quietly missing an
// attribute. So both rules are pinned here.

func irAttrs(t *testing.T, raw string) []any {
	t.Helper()
	var attrs []any
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return attrs
}

func names(attrs []any) []string {
	var out []string
	for _, entry := range attrs {
		attr, _ := entry.(map[string]any)
		name, _ := attr["name"].(string)
		out = append(out, name)
	}
	return out
}

func TestDropAcknowledgementKeepsWhatTheRecordCarries(t *testing.T) {
	attrs := irAttrs(t, `[
		{"name": "sid", "string": {"computed_optional_required": "computed"}},
		{"name": "account_sid", "string": {"computed_optional_required": "computed"}},
		{"name": "name", "string": {"computed_optional_required": "required"}}
	]`)

	record := schemaObject{Properties: map[string]schemaObject{
		"account_sid": {Type: "string"},
		"name":        {Type: "string"},
	}}
	ack := schemaObject{Properties: map[string]schemaObject{"sid": {Type: "string"}}}

	got := names(dropAcknowledgement("account", attrs, record, ack))
	want := []string{"account_sid", "name"}
	if len(got) != len(want) {
		t.Fatalf("attributes are %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attributes are %v, want %v", got, want)
		}
	}
}

func TestDropAcknowledgementKeepsAPropertyBothDeclare(t *testing.T) {
	// The condition is both halves at once. An API whose create response and record
	// share a property has a real attribute reached two ways, and dropping it on
	// the acknowledgement's say-so would delete it from the schema.
	attrs := irAttrs(t, `[{"name": "id", "string": {"computed_optional_required": "computed"}}]`)

	record := schemaObject{Properties: map[string]schemaObject{"id": {Type: "string"}}}
	ack := schemaObject{Properties: map[string]schemaObject{"id": {Type: "string"}}}

	if got := names(dropAcknowledgement("thing", attrs, record, ack)); len(got) != 1 {
		t.Errorf("attributes are %v; a property the record also carries must stay", got)
	}
}

func TestCollapseDuplicatesPrefersWhatTheCallerAsksFor(t *testing.T) {
	// One value reached two ways: the record's identity field and the path
	// parameter addressing the record by it. A resource keeps the server-owned
	// entry; a data source keeps the lookup key the practitioner supplies.
	attrs := irAttrs(t, `[
		{"name": "account_sid", "string": {"computed_optional_required": "required"}},
		{"name": "name", "string": {"computed_optional_required": "computed"}},
		{"name": "account_sid", "string": {"computed_optional_required": "computed"}}
	]`)

	for _, tc := range []struct{ prefer, want string }{
		{"computed", "computed"},
		{"required", "required"},
	} {
		got := collapseDuplicates("account", append([]any(nil), attrs...), tc.prefer)
		if len(got) != 2 {
			t.Fatalf("prefer %q: %d attributes, want 2 — the duplicate was not collapsed", tc.prefer, len(got))
		}
		// Order is the order of first appearance, so the collapsed attribute keeps
		// its position and the generated schema stays stable across runs.
		if n := names(got); n[0] != "account_sid" || n[1] != "name" {
			t.Errorf("prefer %q: attributes are %v, want [account_sid name]", tc.prefer, n)
		}
		if d := attrDisposition(got[0].(map[string]any)); d != tc.want {
			t.Errorf("prefer %q: kept the %q entry, want %q", tc.prefer, d, tc.want)
		}
	}
}
