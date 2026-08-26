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

// Same sort the openapi-format CLI applies by default, so the reviewable YAML
// keeps its stable key order across regenerations.
const {data: sorted} = await openapiFormat.openapiSort(data, {sortSet: await openapiFormat.getDefaultSortSet()});

await openapiFormat.writeFile(yamlOut, sorted, {format: 'yaml', lineWidth: -1});
await openapiFormat.writeFile(jsonOut, sorted, {format: 'json'});

console.log(`wrote ${yamlOut}`);
console.log(`wrote ${jsonOut}`);
