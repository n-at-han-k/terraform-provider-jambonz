# codegen/tooling

Everything that turns one OpenAPI description into a Terraform provider. No API
logic lives here — only statements about how the API should be interpreted.

| File | Stage | What it decides |
| --- | --- | --- |
| `schemas/terraform.overlay.yaml` | 3 | What the API *means*: canonical operation names, what's read-only, what's a secret, where the spec and the implementation disagree |
| `scripts/overlay.mjs` | 3 | Applies the overlay, and fails when an action resolves nothing |
| `generator_config.yml` | 4a | Which operations form one resource's CRUD lifecycle |
| `cmd/gen/normalize.go` | 4a′ | Removes the two artefacts of tfplugingen-openapi's merge |
| `templates/resource.go.tmpl` | 4d | The CRUD glue tfplugingen-framework doesn't generate |
| `templates/data_source.go.tmpl` | 4d | The same, for a lookup |
| `templates/_shared.tmpl` | 4d | The conversion fragments both render |
| `cmd/gen/binding.go` | 4d | The wire contract: operation names, body property sets, success statuses, response shapes |
| `oapi-codegen.yaml` | 4c | Where the HTTP client lands |

## An overlay that resolves nothing is a failed build

openapi-format applies an overlay whose every target has moved and exits 0.
`openapiOverlay` returns `totalUnusedActions`, the CLI prints it only under
`--verbose`, and it exits 0 either way — so an overlay that applies nothing
reports success and leaves a file that reads in review like a contract while doing
nothing at all. A silently inert overlay is worse than a missing one, because a
missing one is visible.

`scripts/overlay.mjs` is why stage 3 is a script rather than a bare
`openapi-format --overlayFile`. openapi-format is still the implementation — the
script calls its exported `openapiOverlay`, so the overlay document stays
tool-neutral — but any unused action is now a named, non-zero failure. The script
also writes the document twice: YAML to read, and JSON for `cmd/gen`.

## The naming contract

`cmd/gen` looks each operation up **by operationId**, as:

    create<Resource>  list<Resource>s  get<Resource>  update<Resource>  delete<Resource>

Most of the jambonz spec is already written that way; the overlay renames the two
that are not (`provisionPhoneNumber`, `listProvisionedPhoneNumbers`). The name has
to be right because oapi-codegen derives both the client method
(`CreateVoipCarrierWithResponse`) and the request body type
(`CreateVoipCarrierJSONRequestBody`) from it, and this generator names them back.

Everything else is read, not assumed:

- which properties each request body carries and which are required — create and
  update do not agree here, and a read-only property is never sent whatever body
  declares it;
- the success status per operation, from the lowest 2xx it declares;
- **what each write answers with**, which is the fact that shapes the whole of the
  generated Create and Update (below);
- the id attribute, snake-cased from the read operation's path parameter — not
  `"id"`, because this API names the parameter after the record;
- whether each path parameter declares `format: uuid`, because some of this API's
  do and some do not, and oapi-codegen types the argument accordingly;
- whether each response field is a pointer, and whether it is a `time.Time`.

A resource whose operations cannot be resolved this way is a fatal error in
`cmd/gen`. That is the point: the failure belongs next to the two files that
disagree, not in `go build` two stages downstream.

## Write, then read

The generator this was ported from assumed a create that answers with the record
it created. jambonz answers `{"sid": "…"}` — `SuccessfulAdd` — and its updates
answer a bare 204. So `binding.go` classifies each write into one of three shapes:

| The write answers with | What the glue does |
| --- | --- |
| the record | decode it, `apply` it |
| an acknowledgement (one property) | take the id from it, then read the record back |
| nothing (204) | read the record back |

The read-back is not belt and braces. Terraform requires every value to be known
once apply returns, and every column the server defaulted is unknown until the
record is read. Both shapes are covered by tests, because the happy path compiles
either way and a regression shows up only as glue that reads a response field
oapi-codegen never populates.

## Why there is a hand-rolled generator at all

`tfplugingen-framework` is still tech preview and generates schema and data
modelling code only. The methods that call the API — Create, Read, Update, Delete,
ImportState — are left to you.

Writing them by hand per resource works until you have five resources and a subtly
different 404 in three of them. So `cmd/gen` reads the same intermediate
representation the framework generator consumes, `provider_code_spec.json`, and
renders them. HashiCorp designed the IR to be extended this way: it is a
documented, versioned artifact, not an internal detail.

Two things fall out of the same binding. `ModifyPlan` is generated for exactly the
attributes the create body accepts and the update body does not — without it,
changing one plans as an in-place update that sends a body with no such property,
the API stores nothing, and the next read shows the old value back. And a data
source decodes a record through the same fragments the resource does, so a lookup
and a managed record cannot disagree about how a field is read.

## Stage 4a′, and why the IR is edited at all

tfplugingen-openapi builds a resource's attributes by merging the create body, the
create response, the read response and the read operation's parameters — by
concatenation. For an API whose create returns the record that is right. Here it
produces `sid` (from the acknowledgement; no record has it) and two `account_sid`
entries (the record's identity field, and the path parameter that addresses the
record by it). A duplicate attribute name is not a schema at all: the framework
keys attributes by name, so the generated file has the same map key twice.

`normalize.go` removes both, between the two HashiCorp generators, on facts read
out of the OpenAPI document — what the create response declares, and what the
record carries. Nothing is dropped on a name pattern, every removal is printed,
and a duplicate whose two entries disagree about type is a fatal error rather than
a coin toss.

## The one rule

`cmd/gen` only ever writes files it owns: `<resource>_resource.go` and
`<name>_data_source.go` in `internal/provider`. The hand-written files in the same
directory — `provider.go`, `convert.go` — are never touched. That is what lets
generated and authored code share a tree without a `generated/` directory nobody
wants to import from.

Output the language would reject is never written: everything goes through
`go/format`, which parses it. The rejected source lands at `<path>.broken` and the
command exits non-zero, so a template that closes a `{{ range }}` in the wrong
place is a failed `make gen` and not a mystery compile error.

## Running it

```bash
# from the repository root
make gen       # stages 3 through 4d
make spec      # stage 3 only
make verify    # regenerate everything and fail on any diff

# or directly, if the contracts are already built
cd codegen/tooling && go run ./cmd/gen -normalize
cd codegen/tooling && go run ./cmd/gen
```

`cmd/gen` depends on nothing outside the standard library, on purpose: the
generator should not be breakable by a dependency bump in the thing it generates.
That is also why stage 3 writes `build/openapi.tf.json` beside the YAML — the
Terraform-shaped document is read here with `encoding/json` rather than by adding
a YAML parser to a package whose `go.mod` has no `require` block.
