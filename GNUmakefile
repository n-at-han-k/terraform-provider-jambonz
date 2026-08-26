# One OpenAPI description in, a Terraform provider out.
#
#   3   overlay        codegen/openapi.yaml + the overlay -> build/openapi.tf.{yaml,json}
#   4a  IR             tfplugingen-openapi                -> codegen/tooling/provider_code_spec.json
#   4a' normalise      cmd/gen -normalize                 -> the same IR, minus the merge artefacts
#   4b  schema+models  tfplugingen-framework              -> internal/resource_*, internal/datasource_*
#   4c  http client    oapi-codegen                       -> internal/api/jambonzapi
#   4d  CRUD glue      codegen/tooling/cmd/gen            -> internal/provider/*_resource.go
#
# `make gen` runs 3 through 4d. Nothing downstream of stage 3 reads the original
# spec, and nothing but cmd/gen writes the files cmd/gen owns.

SPEC         := codegen/openapi.yaml
TF_SPEC      := build/openapi.tf.yaml
TF_SPEC_JSON := build/openapi.tf.json
OVERLAY      := codegen/tooling/schemas/terraform.overlay.yaml
OVERLAY_APPLY := codegen/tooling/scripts/overlay.mjs
CODE_SPEC    := codegen/tooling/provider_code_spec.json

GO   ?= go
NPM  ?= npm

TFPLUGINGEN_OPENAPI_VERSION   ?= v0.3.0
TFPLUGINGEN_FRAMEWORK_VERSION ?= v0.4.1
OAPI_CODEGEN_VERSION          ?= v2.8.0

TOOLS := $(CURDIR)/.tools/bin
export PATH := $(TOOLS):$(PATH)

.DEFAULT_GOAL := gen

.PHONY: tools
tools: $(TOOLS)/tfplugingen-openapi $(TOOLS)/tfplugingen-framework

$(TOOLS)/tfplugingen-openapi:
	CGO_ENABLED=0 GOBIN=$(TOOLS) $(GO) install \
	  github.com/hashicorp/terraform-plugin-codegen-openapi/cmd/tfplugingen-openapi@$(TFPLUGINGEN_OPENAPI_VERSION)

$(TOOLS)/tfplugingen-framework:
	CGO_ENABLED=0 GOBIN=$(TOOLS) $(GO) install \
	  github.com/hashicorp/terraform-plugin-codegen-framework/cmd/tfplugingen-framework@$(TFPLUGINGEN_FRAMEWORK_VERSION)

node_modules: package.json package-lock.json
	$(NPM) install
	@touch $@

$(TF_SPEC) $(TF_SPEC_JSON) &: $(SPEC) $(OVERLAY) $(OVERLAY_APPLY) | node_modules
	mkdir -p build
	node $(OVERLAY_APPLY) $(SPEC) $(OVERLAY) $(TF_SPEC) $(TF_SPEC_JSON)

.PHONY: spec
spec: $(TF_SPEC) $(TF_SPEC_JSON)

$(CODE_SPEC): $(TF_SPEC) codegen/tooling/generator_config.yml | tools
	tfplugingen-openapi generate \
	  --config codegen/tooling/generator_config.yml \
	  --output $@ $(TF_SPEC)

.PHONY: gen
gen: $(CODE_SPEC) $(TF_SPEC_JSON) | tools
	cd codegen/tooling && GOWORK=off $(GO) run ./cmd/gen -normalize
	tfplugingen-framework generate all \
	  --input $(CODE_SPEC) --output internal
	CGO_ENABLED=0 $(GO) run \
	  github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) \
	  --config codegen/tooling/oapi-codegen.yaml $(TF_SPEC)
	cd codegen/tooling && GOWORK=off $(GO) run ./cmd/gen

.PHONY: build
build: gen
	CGO_ENABLED=0 $(GO) build -o terraform-provider-jambonz .

.PHONY: install
install: gen
	$(GO) install -v ./...

.PHONY: docs
docs: gen
	cd tools && $(GO) generate ./...

.PHONY: test
test: gen
	$(GO) test ./...
	cd codegen/tooling && GOWORK=off $(GO) test ./...

.PHONY: testacc
testacc: gen
	TF_ACC=1 $(GO) test -v -cover -timeout 120m ./...

.PHONY: fmt
fmt:
	gofmt -s -w -e .

.PHONY: lint
lint:
	golangci-lint run

# Regeneration is reproducible, or the committed output is a lie.
.PHONY: verify
verify: spec gen docs
	git diff HEAD --exit-code
	@untracked=$$(git ls-files --others --exclude-standard -- \
	   codegen internal docs); \
	 if [ -n "$$untracked" ]; then \
	   echo 'regeneration produced files that are not committed:'; \
	   echo "$$untracked"; \
	   exit 1; \
	 fi

.PHONY: clean
clean:
	rm -rf build $(TOOLS)
