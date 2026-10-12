default: build

.PHONY: build install lint generate docs fmt fmt-examples misspell validate-docs check-descriptions docs-check test test-contract testacc testacc-real sweep

build:
	go build ./...

install:
	go install

lint:
	golangci-lint run

# Generate documentation via tfplugindocs (see tools/tools.go).
generate:
	cd tools; go generate ./...

# Alias for `generate` — regenerate the registry docs under docs/.
docs: generate

# Check that examples/ is canonically formatted (CI gate).
fmt-examples:
	terraform fmt -check -recursive examples/

# Spell-check docs, examples, and top-level markdown (CI gate).
misspell:
	go run github.com/client9/misspell/cmd/misspell@v0.3.4 -error docs/ examples/ *.md

# Validate the generated docs against tfplugindocs' registry rules (CI gate).
validate-docs:
	cd tools; go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs validate --provider-name hubspot --provider-dir ..

# Assert every schema attribute carries a description (CI gate). Needs
# terraform (or tofu via TF_BIN) and jq on PATH.
check-descriptions:
	./tools/check-schema-descriptions.sh

# Aggregate documentation quality gate: regenerate + validate + fmt + spell + coverage.
docs-check: generate validate-docs fmt-examples misspell check-descriptions

fmt:
	gofmt -s -w -e .

test:
	go test ./... -count=1 -timeout=5m

# Validate the hermetic fakes against HubSpot's published OpenAPI specs
# (kin-openapi). Downloads the pinned spec at run time — it is proprietary and
# never vendored; set HUBSPOT_OPENAPI_SPEC_FILE to use a local copy.
test-contract:
	HUBSPOT_OPENAPI_CONTRACT=1 go test ./internal/provider/ -run 'Contract' -v -count=1 -timeout 5m

# Run acceptance tests.
testacc:
	TF_ACC=1 go test ./... -v -count=1 -timeout 120m

# Run acceptance tests against a real HubSpot portal. Requires TF_ACC=1 (set
# below), HUBSPOT_ACCESS_TOKEN (dedicated test portal private-app token) and
# HUBSPOT_TEST_PORTAL_ID; see internal/provider/real_api_test.go for the
# env contract and portal safety guard.
testacc-real:
	TF_ACC=1 go test ./internal/provider/ -run 'TestAccReal' -v -count=1 -timeout 30m

sweep: ## Delete leaked tf_acc_test_* resources from the real test portal
	go test ./internal/provider/ -v -sweep=all -timeout 10m
