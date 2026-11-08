default: build

GO ?= go
SHELL := /bin/bash
TFPLUGINDOCS := $(GO) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs

# Windows without make: Invoke-Build <Task> (terraform-provider-docker.build.ps1)
# runs the same commands (Build, Vet, Test, Docs, GenSkill, Check, TestAcc, E2E,
# Fmt, Install, Compose2Stack -In/-Out/-Mode, Parity, ApiWatch; Invoke-Build ? lists them).

# compose2stack: make compose2stack IN=docker-compose.yml OUT=stack.tf MODE=swarm
IN ?= docker-compose.yml
OUT ?= stack.tf
MODE ?= swarm

.PHONY: build install vet test testacc e2e harness-up harness-down skill docs check lint fmt compose2stack parity apiwatch

# Build the provider binary into the repo root.
build:
	$(GO) build ./...
	$(GO) build -o . .

# Install into $(go env GOBIN) or $(go env GOPATH)/bin, the dev_overrides path.
install:
	$(GO) install .

vet:
	$(GO) vet ./...

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

# Render .claude/skills/terraform-provider-docker/, .claude/skills/docker-cli-map/SKILL.md and README_resources.md from the provider schema.
skill:
	$(GO) run ./tools/genskill

# Regenerate docs/ from the schema, examples/ and templates/. Depends on skill
# so a release can never ship a stale skill.
docs: skill
	$(TFPLUGINDOCS) generate --provider-name docker
	$(TFPLUGINDOCS) validate --provider-name docker

# Everything CI checks besides build and test: formatting, vet, skill and
# README_resources.md drift, RequiresReplace attributes that do not say
# "Forces replacement." in their description, and docs validity.
check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed (make fmt):"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GO) run ./tools/genskill -check
	$(GO) run ./tools/genskill -strict
	$(TFPLUGINDOCS) validate --provider-name docker

lint:
	$(GO) vet ./...
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fmt:
	gofmt -w .

# Convert a compose file to HCL once (tools/compose2stack); the .tf is the source of truth after.
compose2stack:
	$(GO) run ./tools/compose2stack -in $(IN) -out $(OUT) -mode $(MODE)

# kreuzwerker/docker v4.6.0 types vs ours (have / partial / missing). CI runs
# `go run ./tools/parity -check BUILD_REPORT.md`, which also fails when a missing
# type has no [BLOCKED] line in BUILD_REPORT.md.
parity:
	$(GO) run ./tools/parity

# Pinned Engine API spec (tools/apiwatch/pinned.json) vs the newest published one:
# new / changed / removed endpoints and fields mapped to provider types. CI runs
# `go run ./tools/apiwatch -check`, which fails when a newer spec is published.
# See .claude/skills/engine-api-watch/SKILL.md.
apiwatch:
	$(GO) run ./tools/apiwatch
