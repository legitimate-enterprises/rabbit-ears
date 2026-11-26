# Server targets; the root Makefile fans out here.
.DEFAULT_GOAL := help
.PHONY: help deps generate build dist run test test-unit test-integration test-race style lint drift clean

PORT ?= 8477

help: ## Print this target list
	@grep -hE '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

# Without Homebrew, which installs the latest, golangci-lint is this one.
GOLANGCI_LINT_VERSION := v2.15.0

# Where `go install` puts tools; the PATH may lack it, so it is searched last.
TOOLS_BIN = $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

deps: ## Require ffmpeg, install golangci-lint (Homebrew, else go install), download Go modules, generate API code
	@command -v ffmpeg >/dev/null && command -v ffprobe >/dev/null \
		|| { echo "ffmpeg and ffprobe are required (README Building)" >&2; exit 1; }
	export PATH="$$PATH:$(TOOLS_BIN)"; command -v golangci-lint >/dev/null \
		|| { command -v brew >/dev/null && brew install golangci-lint; } \
		|| go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go mod download
	$(MAKE) --no-print-directory generate

generate: ## Regenerate api/api.gen.go from api/openapi.yaml
	go generate ./api

# Regenerating first means the handlers only compile if they implement the
# spec as it stands. The web client rides in the binary; a plain `go build`
# embeds whatever the last `make build` left, or nothing.
build: generate ## Generate from the spec, build the web client into the binary, compile into build/
	$(MAKE) --no-print-directory -C ../web build
	rsync -a --delete --exclude .gitignore ../web/dist/ internal/webui/site/
	go build -o build/rabbit-ears ./cmd/rabbit-ears

VERSION = $(shell sed -n 's/^  version: *//p' api/openapi.yaml | head -1)
DIST_PLATFORMS := darwin/arm64 linux/amd64 linux/arm64

# One archive per platform, each the binary and the license, and the
# checksums of all of them. Without cgo every platform builds on any host,
# and -trimpath keeps the host's paths out of the binary. With
# CODESIGN_IDENTITY set the macOS binary is signed: macOS ties a permission
# it granted the server, such as reading the media volume, to the signature,
# and asks again for every unsigned build.
dist: build ## Release archives into build/dist/ (CODESIGN_IDENTITY signs the macOS binary)
	rm -rf build/dist build/stage
	mkdir -p build/dist
	@set -e; for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=rabbit-ears-$(VERSION)-$$os-$$arch; \
		mkdir -p build/stage/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o build/stage/$$name/rabbit-ears ./cmd/rabbit-ears; \
		if [ $$os = darwin ] && [ -n "$$CODESIGN_IDENTITY" ]; then \
			codesign --force --identifier enterprises.legitimate.rabbit-ears.server \
				--sign "$$CODESIGN_IDENTITY" build/stage/$$name/rabbit-ears; \
		fi; \
		cp ../LICENSE build/stage/$$name/; \
		COPYFILE_DISABLE=1 tar -C build/stage -czf build/dist/$$name.tar.gz $$name; \
		echo "build/dist/$$name.tar.gz"; \
	done
	cd build/dist && shasum -a 256 *.tar.gz > SHA256SUMS

run: ## Run the server on PORT (default 8477)
	RABBIT_EARS_PORT=$(PORT) go run ./cmd/rabbit-ears

test: test-integration ## Every tier

test-unit: ## go test without the tests that need ffmpeg or fixtures/
	go test -short ./...

# Every test, the unit tier included: `go test` has no way to run only the
# rest.
test-integration: ## go test with ffmpeg against fixtures/, fetched when missing
	../scripts/fetch-fixtures.sh
	go test ./...

# Not a CI tier: the race detector instruments the SQLite driver, itself Go,
# so a package that queries much runs about fifteen times slower.
test-race: ## Every test under the race detector; a quarter of an hour on a fast Mac
	../scripts/fetch-fixtures.sh
	go test -race -timeout 45m ./...

style: ## gofmt in checking mode
	@out="$$(gofmt -l .)"; [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }

lint: drift ## golangci-lint and spec drift
	PATH="$$PATH:$(TOOLS_BIN)" golangci-lint run ./...

drift: ## Fail if api/api.gen.go is stale against api/openapi.yaml
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	sed "s|^output:.*|output: $$tmp/api.gen.go|" api/oapi-codegen.yaml > "$$tmp/config.yaml" && \
	(cd api && go tool oapi-codegen -config "$$tmp/config.yaml" openapi.yaml) && \
	cmp -s "$$tmp/api.gen.go" api/api.gen.go || { echo "api/api.gen.go is stale; run make generate" >&2; exit 1; }

clean: ## Remove build products and the embedded web client
	rm -rf build
	find internal/webui/site -mindepth 1 ! -name .gitignore -delete
