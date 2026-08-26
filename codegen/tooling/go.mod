module terraform-provider-jambonz/codegen/tooling

go 1.23

// cmd/gen deliberately depends on nothing outside the standard library: it
// reads provider_code_spec.json and renders text/template. Keeping it
// dependency-free means the generator cannot be broken by an unrelated
// dependency bump in the provider or the CLI.
