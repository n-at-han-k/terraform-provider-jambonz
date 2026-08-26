// Command gen renders the code that tfplugingen-framework does not.
//
// tfplugingen-framework is still tech preview and generates schema and data
// modelling code only. That leaves the CRUD glue — the part that actually calls
// the API — unwritten. Rather than hand-write it per resource, we render it
// from the same intermediate representation the framework generator consumes:
// provider_code_spec.json. HashiCorp designed the IR to be extended this way.
//
// The same IR drives the CLI commands, which is why `tradeportal prod create` and
// the provider's Create take exactly the same fields. One input, two outputs, no
// drift.
//
// # Why this command also reads the OpenAPI document
//
// The Terraform IR describes a *schema*. It says nothing about the Go client the
// glue has to call: not the method names, not the request body types, not which
// properties each body actually carries, not what a success response looks like.
// The templates used to assume all four, from the shape of the hand-written demo
// spec that shipped with this repository — `CreateDatabaseWithResponse`, a flat
// `Database` response, one body schema serving both create and update, every
// attribute a string.
//
// A Grape-generated spec breaks every one of those assumptions, and it broke them
// silently: `make gen` succeeded and `make build` failed with
// `r.client.CreateProductWithResponse undefined`. So the assumptions are gone. The
// overlay assigns canonical operationIds, and this command resolves the rest out
// of build/openapi.tf.json — the same document tfplugingen-openapi and oapi-codegen
// read — and fails loudly when the contract is not met. binding.go is that
// contract.
//
//	go run ./cmd/gen
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Trimmed view of provider_code_spec.json — only the parts the templates read.
// The full schema lives in terraform-plugin-codegen-spec.
type Specification struct {
	Provider    Provider   `json:"provider"`
	Resources   []Resource `json:"resources"`
	DataSources []Resource `json:"datasources"`
}

type Provider struct {
	Name string `json:"name"`
}

type Resource struct {
	Name   string `json:"name"`
	Schema Schema `json:"schema"`
}

type Schema struct {
	Attributes []Attribute `json:"attributes"`
}

// HasComputed reports whether any attribute is purely server-owned. The update
// glue seeds unknown computed values from prior state, and with none to seed the
// prior state is not read at all — so the template needs to know, or it emits a
// declared-and-not-used variable.
func (r Resource) HasComputed() bool {
	for _, a := range r.Schema.Attributes {
		if a.Computed() {
			return true
		}
	}
	return false
}

// HasNested reports whether any attribute is an object. The nested conversions
// are the only thing that needs the framework's attr package, and an import Go
// would reject as unused is not worth a template that guesses.
func (r Resource) HasNested() bool {
	for _, a := range r.Schema.Attributes {
		if a.Type == "single_nested" {
			return true
		}
	}
	return false
}

// ForceNew is the attributes whose change replaces the record.
func (r Resource) ForceNew() []Attribute {
	var out []Attribute
	for _, a := range r.Schema.Attributes {
		if a.ForceNew() {
			out = append(out, a)
		}
	}
	return out
}

// Attribute is a union in the IR: exactly one of the typed fields is set. We
// normalise it in UnmarshalJSON so the templates see a flat shape.
//
// The fields below the line are not in the IR at all — they are the wire facts
// Document.bind resolves from the OpenAPI document, and they are what decides
// whether a field is sent, how it is sent, and whether it can be read back.
type Attribute struct {
	// Resource is the owning resource's name. It is only used in messages now:
	// nothing here derives a Go type name from it any more, because
	// oapi-codegen's type names come from the *schema* a property was reached
	// through and not from the operation. Guessing them was the bug.
	Resource string `json:"-"`

	Name string `json:"name"`

	// Normalised from the IR.
	Type                     string `json:"-"`
	ComputedOptionalRequired string `json:"-"`
	Description              string `json:"-"`
	Sensitive                bool   `json:"-"`
	Default                  string `json:"-"`

	// ---- resolved from the OpenAPI document --------------------------------
	InCreate       bool `json:"-"`
	CreateRequired bool `json:"-"`
	InUpdate       bool `json:"-"`
	UpdateRequired bool `json:"-"`
	InResponse     bool `json:"-"`
	// The three UUID flags are per body, not per attribute: `format: uuid` makes
	// oapi-codegen emit openapi_types.UUID, and a property can carry the format in
	// the record and not in a request body. Terraform has no UUID type, so each
	// side converts separately.
	CreateUUID   bool `json:"-"`
	UpdateUUID   bool `json:"-"`
	ResponseUUID bool `json:"-"`
	// Nested is the attributes of a single_nested object — a webhook here. They
	// carry their own wire facts, resolved against the sub-schema the property
	// $refs in each of the three bodies.
	Nested []Attribute `json:"-"`
	// ResponsePointer is how oapi-codegen renders the response field: a pointer
	// when the property is optional or readOnly. Guessed before, from
	// "computed ⇔ pointer"; read from the document now.
	ResponsePointer bool `json:"-"`
	// ResponseTime is `format: date-time`, which oapi-codegen renders as
	// time.Time rather than string. Inferred from a *_at name suffix before.
	ResponseTime bool `json:"-"`
}

type attrBody struct {
	ComputedOptionalRequired string `json:"computed_optional_required"`
	Description              string `json:"description"`
	Sensitive                bool   `json:"sensitive"`
	Default                  *struct {
		Static *string `json:"static"`
	} `json:"default"`
	Attributes []Attribute `json:"attributes"`
}

func (a *Attribute) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if n, ok := raw["name"]; ok {
		if err := json.Unmarshal(n, &a.Name); err != nil {
			return err
		}
		delete(raw, "name")
	}
	for typ, body := range raw {
		var v attrBody
		if err := json.Unmarshal(body, &v); err != nil {
			return fmt.Errorf("attribute %q (%s): %w", a.Name, typ, err)
		}
		a.Type = typ
		a.ComputedOptionalRequired = v.ComputedOptionalRequired
		a.Description = v.Description
		a.Sensitive = v.Sensitive
		a.Nested = v.Attributes
		// The OpenAPI `default` survives all the way here, so the CLI flag can
		// advertise the same default the API applies.
		if v.Default != nil && v.Default.Static != nil {
			a.Default = *v.Default.Static
		}
		break
	}
	return nil
}

// Required reports whether Terraform requires the attribute in configuration.
// That is the schema's opinion, and it is a different question from whether the
// create body requires the property — see CreateRequired.
func (a Attribute) Required() bool { return a.ComputedOptionalRequired == "required" }

// ForceNew reports whether changing the attribute has to replace the record: the
// create body accepts it and the update body does not, so an in-place update
// would silently drop the change and the next read would report drift.
func (a Attribute) ForceNew() bool { return a.InCreate && !a.InUpdate && !a.Computed() }

// Computed reports whether the attribute is purely server-owned — unknown at
// plan time, and never present in the configuration.
func (a Attribute) Computed() bool { return a.ComputedOptionalRequired == "computed" }

func main() {
	var (
		specPath    = flag.String("spec", "provider_code_spec.json", "path to provider_code_spec.json")
		openAPIPath = flag.String("openapi", "../../build/openapi.tf.json", "path to the Terraform-shaped OpenAPI document, as JSON")
		provDir     = flag.String("provider-dir", "../../internal/provider", "output dir for Terraform CRUD glue")
		tmplDir     = flag.String("templates", "templates", "template directory")
		fixturePath = flag.String("fixtures", "schemas/acceptance.json", "path to the acceptance-test fixtures")
		normalizeIR = flag.Bool("normalize", false, "rewrite the IR in place instead of rendering (stage 4a\u2032; see normalize.go)")
	)
	flag.Parse()

	if *normalizeIR {
		normalize(*specPath, readDocument(*openAPIPath))
		return
	}

	funcs := template.FuncMap{
		"pascal":      pascal,
		"camel":       camel,
		"respGet":     respGet,
		"respGetFrom": respGetFrom,
		"reqSet":      reqSet,
		"reqSetField": reqSetField,
		"nestedDst":   nestedDst,
		"nullValue":   nullValue,
		"join":        strings.Join,
		"dict":        dict,
	}
	renderProvider(readSpec(*specPath), readDocument(*openAPIPath), readFixtures(*fixturePath), funcs, *tmplDir, *provDir)
}

func readSpec(path string) Specification {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read spec: %v", err)
	}
	var spec Specification
	if err := json.Unmarshal(raw, &spec); err != nil {
		log.Fatalf("parse spec: %v", err)
	}
	return spec
}

// resourceData is what one resource's templates see: the IR, the provider name and
// the wire binding resolved from the OpenAPI document.
type resourceData struct {
	Resource
	Provider string
	Wire     Binding
	// Fixture is the acceptance-test fixture for this resource. Empty for a data
	// source, which the test template is not rendered for.
	Fixture Fixture
}

// renderProvider emits the Terraform CRUD glue, one file per resource in the IR.
func renderProvider(spec Specification, doc Document, fixtures map[string]Fixture, funcs template.FuncMap, tmplDir, provDir string) {
	// The conversion fragments are parsed into both sets: a data source decodes a
	// record exactly as the resource does, and two copies of that would drift.
	shared := filepath.Join(tmplDir, "_shared.tmpl")
	resTmpl := template.Must(template.New("resource.go.tmpl").Funcs(funcs).
		ParseFiles(filepath.Join(tmplDir, "resource.go.tmpl"), shared))
	dsTmpl := template.Must(template.New("data_source.go.tmpl").Funcs(funcs).
		ParseFiles(filepath.Join(tmplDir, "data_source.go.tmpl"), shared))
	testTmpl := template.Must(template.New("resource_test.go.tmpl").Funcs(funcs).
		ParseFiles(filepath.Join(tmplDir, "resource_test.go.tmpl"), shared))

	for _, r := range spec.Resources {
		for i := range r.Schema.Attributes {
			r.Schema.Attributes[i].Resource = r.Name
		}
		// This is where the pipeline's two halves are checked against each other.
		// Anything bind cannot resolve is fatal here: a template that guesses
		// produces code that fails to compile two stages later, which is exactly
		// how this generator failed before.
		wire, err := doc.bind(r.Name, r.Schema.Attributes)
		if err != nil {
			log.Fatalf("resource %q: %v", r.Name, err)
		}
		// A resource with no fixture has no acceptance test, and that is a
		// decision someone has to make rather than an omission that goes
		// unnoticed: every resource in the IR is covered, or this fails.
		fixture, ok := fixtures[r.Name]
		if !ok {
			log.Fatalf("resource %q: no entry in the acceptance fixtures; add one to schemas/acceptance.json", r.Name)
		}
		data := resourceData{Resource: r, Provider: spec.Provider.Name, Wire: wire, Fixture: fixture}

		render(resTmpl, data, filepath.Join(provDir, r.Name+"_resource.go"))
		render(testTmpl, data, filepath.Join(provDir, r.Name+"_resource_test.go"))
	}

	for _, d := range spec.DataSources {
		for i := range d.Schema.Attributes {
			d.Schema.Attributes[i].Resource = d.Name
		}
		wire, err := doc.bindRead(d.Name, d.Schema.Attributes)
		if err != nil {
			log.Fatalf("data source %q: %v", d.Name, err)
		}
		data := resourceData{Resource: d, Provider: spec.Provider.Name, Wire: wire}

		render(dsTmpl, data, filepath.Join(provDir, d.Name+"_data_source.go"))
	}
}

func render(t *template.Template, data any, out string) {
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		log.Fatalf("%s: execute template: %v", out, err)
	}
	src, err := checked(out, []byte(buf.String()))
	if err != nil {
		// Write the rejected source so the syntax error is greppable.
		_ = os.WriteFile(out+".broken", []byte(buf.String()), 0o644)
		log.Fatalf("%s: %v (rejected output at %s.broken)", out, err, out)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		log.Fatalf("%s: mkdir: %v", out, err)
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		log.Fatalf("%s: write: %v", out, err)
	}
	fmt.Println("wrote", out)
}

// checked refuses to write output the language would reject, per extension. Go gets
// go/format, which parses; TypeScript gets the balance check below, which does not.
// An unknown extension is an error rather than a passthrough — silently writing
// unchecked bytes is how a generator starts producing garbage nobody notices.
func checked(out string, src []byte) ([]byte, error) {
	switch ext := filepath.Ext(out); ext {
	case ".go":
		formatted, err := format.Source(src)
		if err != nil {
			return nil, fmt.Errorf("gofmt: %w", err)
		}
		return formatted, nil
	case ".ts":
		return src, checkTypeScript(src)
	default:
		return nil, fmt.Errorf("no syntax check for %q", ext)
	}
}

// pairs is the closer each opener expects.
var pairs = map[byte]byte{'{': '}', '(': ')', '[': ']'}

// checkTypeScript is a bracket balance check that knows about comments and string
// literals. It is **not** a parse, and it is not pretending to be one: there is no
// TypeScript parser in the Go standard library and cmd/gen depends on nothing outside
// it on purpose (see codegen/tooling/go.mod). `yarn workspace native tsc` is what
// type-checks the result.
//
// What it does catch is the failure mode a template actually has: a `{{ range }}` that
// closes in the wrong place, or a `{{ if }}` that swallows a brace. That produces a
// file whose braces do not balance, and this turns it into a .broken file and a failed
// `make gen` rather than an import error in Metro three commits later.
func checkTypeScript(src []byte) error {
	var (
		want []byte
		line = 1
	)
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\n':
			line++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i+1 < len(src) && src[i+1] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end, lines, ok := skipBlockComment(src, i+2)
			if !ok {
				return fmt.Errorf("line %d: unterminated block comment", line)
			}
			i, line = end, line+lines
		case c == '\'' || c == '"' || c == '`':
			end, lines, ok := skipString(src, i+1, c)
			if !ok {
				return fmt.Errorf("line %d: unterminated %c-quoted string", line, c)
			}
			i, line = end, line+lines
		case pairs[c] != 0:
			want = append(want, pairs[c])
		case c == '}' || c == ')' || c == ']':
			if len(want) == 0 {
				return fmt.Errorf("line %d: stray %c", line, c)
			}
			if want[len(want)-1] != c {
				return fmt.Errorf("line %d: %c closes a %c", line, c, want[len(want)-1])
			}
			want = want[:len(want)-1]
		}
	}
	if len(want) > 0 {
		return fmt.Errorf("%d unclosed bracket(s), expected %q", len(want), want)
	}
	return nil
}

// skipBlockComment returns the index of the closing slash, the newlines crossed, and
// whether the comment was terminated at all.
func skipBlockComment(src []byte, from int) (int, int, bool) {
	lines := 0
	for i := from; i < len(src); i++ {
		if src[i] == '\n' {
			lines++
		}
		if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
			return i + 1, lines, true
		}
	}
	return 0, 0, false
}

// skipString returns the index of the closing quote, the newlines crossed, and whether
// the literal was terminated. A backtick may span lines; a quote may not, which is
// how a template that drops a closing quote is caught on the line it happened rather
// than at the end of the file.
func skipString(src []byte, from int, quote byte) (int, int, bool) {
	lines := 0
	for i := from; i < len(src); i++ {
		switch {
		case src[i] == '\\':
			i++
		case src[i] == '\n' && quote != '`':
			return 0, 0, false
		case src[i] == '\n':
			lines++
		case src[i] == quote:
			return i, lines, true
		}
	}
	return 0, 0, false
}

// pascal must agree with oapi-codegen's field naming exactly — these names index
// into its generated structs. oapi-codegen title-cases each underscore-separated
// part and does not special-case initialisms, so neither do we: org_id -> OrgId,
// api_token -> ApiToken.
func pascal(s string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

func camel(s string) string {
	p := pascal(s)
	if p == "" {
		return p
	}
	return strings.ToLower(p[:1]) + p[1:]
}

// dict builds a map inside a template, so a {{ template }} call can pass more
// than one value to a sub-template. text/template has no other way to do it.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict takes an even number of arguments, got %d", len(pairs))
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %d is a %T, not a string", i, pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}
