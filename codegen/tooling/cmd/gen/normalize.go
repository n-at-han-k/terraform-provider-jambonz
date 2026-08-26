// Stage 4a′: the two edits that make tfplugingen-openapi's output a valid
// Terraform schema for this API.
//
// tfplugingen-openapi builds a resource's attributes by merging four sources —
// the create request body, the create response, the read response, and the read
// operation's parameters — and it merges them by concatenation. That is the right
// default for an API whose create answers with the record it created. jambonz's
// does not: every create answers `{"sid": "…"}` (`SuccessfulAdd`), and every
// record is addressed by a path parameter named after its own identity field. So
// the merge produces two things a provider cannot ship:
//
//	"sid"           computed          — from SuccessfulAdd; no record has this field
//	"account_sid"   computed          — the record's identity field
//	"account_sid"   computed_optional — the {AccountSid} path parameter, snake_cased
//
// A duplicate attribute name is not a schema at all: terraform-plugin-framework
// keys attributes by name, so tfplugingen-framework emits the same map key twice
// and the file does not compile. And `sid` is an attribute the practitioner would
// see, could not set, and that no read would ever populate.
//
// Both are artefacts of the merge rather than statements about the API, so they
// are removed here, between the two HashiCorp generators, on facts read out of
// the OpenAPI document: what the create response actually declares, and what the
// record actually carries. Nothing is dropped on a name pattern.
//
//	go run ./cmd/gen -normalize
//
// The IR is walked as generic JSON rather than through the typed Specification
// above, which is deliberately partial: normalising through a partial model would
// silently delete every field this command does not know about.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
)

// normalize rewrites the IR at specPath in place. Every removal is reported on
// stdout: this stage edits a generated artifact that a human reads next, and a
// silent edit there is indistinguishable from a generator bug.
func normalize(specPath string, doc Document) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		log.Fatalf("read %s: %v", specPath, err)
	}
	var ir map[string]any
	if err := json.Unmarshal(raw, &ir); err != nil {
		log.Fatalf("parse %s: %v", specPath, err)
	}

	ops, err := doc.operationByID()
	if err != nil {
		log.Fatalf("%v", err)
	}

	resources, _ := ir["resources"].([]any)
	for _, entry := range resources {
		res, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := res["name"].(string)
		schema, ok := res["schema"].(map[string]any)
		if !ok {
			continue
		}
		attrs, _ := schema["attributes"].([]any)

		record, acknowledgement, err := doc.recordAndAcknowledgement(name, ops)
		if err != nil {
			log.Fatalf("resource %q: %v", name, err)
		}

		attrs = dropAcknowledgement(name, attrs, record, acknowledgement)
		// On a resource the identity field is the server's: the record's computed
		// entry wins and the path parameter's computed_optional entry — which would
		// invite a practitioner to set a server-assigned sid — goes.
		attrs = collapseDuplicates(name, attrs, "computed")
		schema["attributes"] = attrs
	}

	// A data source has the same duplicate for the opposite reason, and resolves it
	// the other way. Its path parameter is the lookup key the practitioner supplies,
	// so the required entry is the one that survives; the record's computed entry is
	// the same value read back.
	datasources, _ := ir["datasources"].([]any)
	for _, entry := range datasources {
		ds, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := ds["name"].(string)
		schema, ok := ds["schema"].(map[string]any)
		if !ok {
			continue
		}
		attrs, _ := schema["attributes"].([]any)
		schema["attributes"] = collapseDuplicates(name, attrs, "required")
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "\t")
	if err := enc.Encode(ir); err != nil {
		log.Fatalf("render %s: %v", specPath, err)
	}
	if err := os.WriteFile(specPath, buf.Bytes(), 0o644); err != nil {
		log.Fatalf("write %s: %v", specPath, err)
	}
	fmt.Println("normalized", specPath)
}

// recordAndAcknowledgement returns the schema of the record — what read answers
// with — and the schema of what create answers with. They are the same document
// for an API that returns the created record, and this stage then has nothing to
// do.
func (d Document) recordAndAcknowledgement(resource string, ops map[string]boundOperation) (schemaObject, schemaObject, error) {
	r := pascal(resource)
	read, ok := ops["get"+r]
	if !ok {
		return schemaObject{}, schemaObject{}, fmt.Errorf("the spec declares no operation with operationId %q", "get"+r)
	}
	create, ok := ops["create"+r]
	if !ok {
		return schemaObject{}, schemaObject{}, fmt.Errorf("the spec declares no operation with operationId %q", "create"+r)
	}

	readBody, err := d.successSchema(read.Op)
	if err != nil {
		return schemaObject{}, schemaObject{}, fmt.Errorf("read operation: %w", err)
	}
	record, _, err := d.resolve(readBody)
	if err != nil {
		return schemaObject{}, schemaObject{}, fmt.Errorf("read response: %w", err)
	}

	createBody, err := d.successSchema(create.Op)
	if err != nil {
		return schemaObject{}, schemaObject{}, fmt.Errorf("create operation: %w", err)
	}
	ack, _, err := d.resolve(createBody)
	if err != nil {
		return schemaObject{}, schemaObject{}, fmt.Errorf("create response: %w", err)
	}
	return record, ack, nil
}

// dropAcknowledgement removes the attributes the create response contributed that
// the record does not carry.
//
// The condition is both halves at once. A property of the create response that
// the record also carries is a real attribute reached through two operations, and
// stays. A property only the acknowledgement declares describes the acknowledgement
// — jambonz's `{"sid": "…"}` — and describes no record, so no read can populate
// it and no practitioner can act on it.
func dropAcknowledgement(resource string, attrs []any, record, ack schemaObject) []any {
	if len(ack.Properties) == 0 {
		return attrs
	}
	kept := attrs[:0]
	for _, entry := range attrs {
		attr, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		name, _ := attr["name"].(string)
		_, inAck := ack.Properties[name]
		_, inRecord := record.Properties[name]
		if inAck && !inRecord {
			fmt.Printf("  %s: dropped %q — declared by the create response, carried by no record\n", resource, name)
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// collapseDuplicates keeps one attribute per name, preferring the disposition the
// caller names.
//
// The duplicate this API produces is one value reached two ways: the record's
// identity field, and the path parameter that addresses the record by it. Which
// of the two should survive is the caller's to say, because it differs between a
// resource and a data source.
//
// Duplicates that disagree about type are not this case and are a fatal error:
// silently picking one would be a guess about the API, which is what this whole
// stage exists to avoid.
func collapseDuplicates(resource string, attrs []any, prefer string) []any {
	byName := map[string][]map[string]any{}
	var order []string
	for _, entry := range attrs {
		attr, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := attr["name"].(string)
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = append(byName[name], attr)
	}

	out := make([]any, 0, len(order))
	for _, name := range order {
		group := byName[name]
		if len(group) == 1 {
			out = append(out, group[0])
			continue
		}

		kinds := map[string]bool{}
		for _, attr := range group {
			kinds[attrKind(attr)] = true
		}
		if len(kinds) > 1 {
			var names []string
			for kind := range kinds {
				names = append(names, kind)
			}
			sort.Strings(names)
			log.Fatalf("resource %q: attribute %q appears %d times with different types (%v).\n"+
				"That is not the merge artefact this stage collapses — it is two different\n"+
				"properties sharing a name across the create body, the response and the path\n"+
				"parameters. Reconcile them in codegen/tooling/schemas/terraform.overlay.yaml.",
				resource, name, len(group), names)
		}

		winner := group[0]
		for _, attr := range group {
			if attrDisposition(attr) == prefer {
				winner = attr
				break
			}
		}
		fmt.Printf("  %s: collapsed %d copies of %q to the %s one\n",
			resource, len(group), name, attrDisposition(winner))
		out = append(out, winner)
	}
	return out
}

// attrKind is the IR's type tag: the single member of the attribute object that
// is not its name.
func attrKind(attr map[string]any) string {
	for key := range attr {
		if key != "name" {
			return key
		}
	}
	return ""
}

func attrDisposition(attr map[string]any) string {
	body, ok := attr[attrKind(attr)].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := body["computed_optional_required"].(string)
	return s
}
