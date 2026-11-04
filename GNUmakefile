default: build

GO ?= go
SHELL := /bin/bash

.PHONY: build install test testacc e2e harness-up harness-down skill docs lint fmt

# Build the provider binary into the repo root.
build:
	$(GO) build -o . ./...

# Install into $(go env GOBIN) or $(go env GOPATH)/bin, the dev_overrides path.
install:
	$(GO) install .

# Unit tests only: no daemon, TestAcc* skip themselves.
test:
	$(GO) test ./... -count=1 -timeout 10m

# Acceptance tests against DOCKER_HOST (skips cleanly when TF_ACC or DOCKER_HOST is unset).
testacc:
	TF_ACC=1 $(GO) test ./internal/provider/ -run TestAcc -count=1 -v -timeout 60m

# Bring up the dind manager + two workers (test/harness), run the end-to-end test, tear down.
e2e: harness-up
	source test/harness/.env && TF_ACC=1 $(GO) test ./internal/provider/ -run TestAccE2E -count=1 -v -timeout 30m; \
	rc=$$?; $(MAKE) harness-down; exit $$rc

harness-up:
	bash test/harness/scripts/harness-up.sh

harness-down:
	bash test/harness/scripts/harness-down.sh

# Render skills/terraform-provider-docker/ from the provider schema.
skill:
	$(GO) run ./tools/genskill

# Regenerate docs/ from the schema, examples/ and templates/. Depends on skill
# so a release can never ship a stale skill.
docs: skill
	$(GO) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name docker
	$(GO) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs validate --provider-name docker

lint:
	$(GO) vet ./...
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fmt:
	gofmt -w .
