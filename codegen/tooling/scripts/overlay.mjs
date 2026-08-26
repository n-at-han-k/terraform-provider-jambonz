// Stage 3: apply codegen/tooling/schemas/terraform.overlay.yaml to
// codegen/openapi.yaml, and refuse to succeed if any action resolved nothing.
//
//   node codegen/tooling/scripts/overlay.mjs <spec> <overlay> <out.yaml> <out.json>
//
// ## Why this is a script and not `openapi-format --overlayFile`
//
// openapi-format is still the implementation — this file calls its exported
// `openapiOverlay` and its own sorter and serialiser, so the overlay document
// stays tool-neutral and swapping implementations is still a change confined to
// one place. What the CLI will not do is fail.
//
// `openapiOverlay` returns `totalUnusedActions`, the CLI prints it only under
// --verbose, and it exits 0 either way. So an overlay whose every target has
// moved applies zero actions, reports success, and leaves a file that reads in
// review like a contract while doing nothing at all. That is precisely what
// happened here: the overlay went on naming the template's demo
// `/api/v1/databases/{id}/query` and `Database` schema long after both were
// deleted, six dead actions, green pipeline. A silently inert overlay is worse
// than a missing one, because a missing one is visible.
//
// So: any unused action is a failed build, named and pointed at.
//
// ## Two outputs of one document
//
// The YAML is what tfplugingen-openapi and oapi-codegen read, and what you open
// when you want to know what the overlay did. The JSON is the same object, for
// codegen/tooling/cmd/gen — which resolves the naming contract out of the spec
// and is deliberately dependency-free, so it has encoding/json and no YAML
// parser. Written from one in-memory document in one pass, so the two cannot
// disagree about what the overlay produced.
import {createRequire} from 'node:module';

const require = createRequire(import.meta.url);
const openapiFormat = require('openapi-format');

const [specPath, overlayPath, yamlOut, jsonOut] = process.argv.slice(2);
if (!specPath || !overlayPath || !yamlOut || !jsonOut) {
  console.error('usage: overlay.mjs <spec> <overlay> <out.yaml> <out.json>');
  process.exit(2);
}

const spec = await openapiFormat.parseFile(specPath);
if (spec instanceof Error) {
  console.error(`cannot read ${specPath}: ${spec.message}`);
  process.exit(1);
}

const overlaySet = await openapiFormat.parseFile(overlayPath);
if (overlaySet instanceof Error) {
  console.error(`cannot read ${overlayPath}: ${overlaySet.message}`);
  process.exit(1);
}

const {data, resultData} = await openapiFormat.openapiOverlay(spec, {overlaySet});

console.log(`overlay ${overlayPath}: ${resultData.totalUsedActions}/${resultData.totalActions} actions applied`);

if (resultData.totalUnusedActions > 0) {
  console.error(`\n${resultData.totalUnusedActions} overlay action(s) resolved no target:\n`);
  for (const action of resultData.unusedActions) {
    const kind = action.remove ? 'remove' : action.update ? 'update' : action.copy ? 'copy' : 'unknown';
    console.error(`  ${kind}  ${action.target}`);
    if (action.description) {
      console.error(`         (${action.description})`);
    }
  }
  console.error(
    '\nAn overlay action that matches nothing is dead weight that reads like a\n' +
      'contract. Either the target moved in the spec — fix the target — or the\n' +
      'statement no longer applies — delete the action.\n'
  );
  process.exit(1);
}

// ## Stage 3b: derive the update bodies that x-immutable implies
//
// The overlay marks a property `x-immutable` when the API accepts it on create
// and refuses it on update — see the "what the server accepts once" section of
// terraform.overlay.yaml for the three, and the implementation lines that
// enforce them.
//
// Saying so is not enough on its own, because every jambonz update is a
// whole-record PUT whose body `$ref`s the record. Marking the property changes
// what cmd/gen *sends*, but oapi-codegen still renders the body from the record
// schema — and a property the record requires becomes a non-pointer Go field
// with no omitempty. It is then serialised whatever the provider does with it,
// as the zero value, and the zero UUID is
//
//   "00000000-0000-0000-0000-000000000000"
//
// which is a perfectly truthy string to `if (req.body.service_provider_sid)`.
// So the update failed with "may not be modified" about a property Terraform
// never set, and no amount of care on the Go side could have avoided it: the
// property has to be absent from the body *schema*.
//
// Hence this pass. Every PUT whose body $refs a schema with x-immutable
// properties gets its own <Name>Update schema — the record, minus those
// properties, minus them from `required`. It is derived rather than written out
// in the overlay because a hand-copy of a forty-property schema is a copy that
// rots the moment the record gains a property, and silently: the copy would
// still be valid, just short.
const immutableProps = (schema) =>
  Object.entries(schema?.properties ?? {})
    .filter(([, prop]) => prop?.['x-immutable'])
    .map(([name]) => name);

let derived = 0;
for (const [path, item] of Object.entries(data.paths ?? {})) {
  const body = item?.put?.requestBody?.content?.['application/json']?.schema;
  const ref = body?.$ref;
  if (!ref?.startsWith('#/components/schemas/')) continue;

  const name = ref.slice('#/components/schemas/'.length);
  const record = data.components?.schemas?.[name];
  const immutable = immutableProps(record);
  if (immutable.length === 0) continue;

  const updateName = `${name}Update`;
  const properties = {...record.properties};
  for (const prop of immutable) delete properties[prop];

  data.components.schemas[updateName] = {
    ...record,
    description:
      `${name}, minus the properties the API refuses on update ` +
      `(${immutable.join(', ')}). Derived by scripts/overlay.mjs from x-immutable; do not edit.`,
    properties,
    required: (record.required ?? []).filter((prop) => !immutable.includes(prop)),
  };
  item.put.requestBody.content['application/json'].schema = {$ref: `#/components/schemas/${updateName}`};
  console.log(`derived ${updateName} for PUT ${path}: dropped ${immutable.join(', ')}`);
  derived++;
}
if (derived > 0) {
  console.log(`derived ${derived} update schema(s) from x-immutable`);
}

// Same sort the openapi-format CLI applies by default, so the reviewable YAML
// keeps its stable key order across regenerations.
const {data: sorted} = await openapiFormat.openapiSort(data, {sortSet: await openapiFormat.getDefaultSortSet()});

await openapiFormat.writeFile(yamlOut, sorted, {format: 'yaml', lineWidth: -1});
await openapiFormat.writeFile(jsonOut, sorted, {format: 'json'});

console.log(`wrote ${yamlOut}`);
console.log(`wrote ${jsonOut}`);
