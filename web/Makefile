# Node comes from the PATH (nvm users: select an LTS first).
.DEFAULT_GOAL := help
.PHONY: help deps generate build run test test-unit test-e2e style lint format clean

help: ## Print this target list
	@grep -hE '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

# On a CI runner the browsers' system libraries are installed too, which
# takes root.
deps: ## Install npm packages from the lockfile, generate the API client
	npm ci
	npx playwright install $(if $(CI),--with-deps) firefox webkit
	$(MAKE) --no-print-directory generate

generate: ## Regenerate src/api/schema.gen.ts from the server's OpenAPI spec
	npm run generate

# The server's build runs this one where only the server's deps ran.
node_modules: package-lock.json
	npm ci

build: node_modules ## Typecheck and bundle both entries into dist/
	npm run build

run: ## Vite dev server (proxies /api to the server)
	npm run dev

test: test-unit test-e2e ## Every tier

test-unit: ## Vitest
	npm run unit

test-e2e: ## Playwright against a real server over fixtures/, fetched when missing
	../scripts/fetch-fixtures.sh
	npx playwright test

style: ## Prettier in checking mode
	npm run style

format: ## Prettier in place
	npm run format

lint: ## ESLint and API client drift against the spec
	npm run lint
	npm run -s drift

clean: ## Remove build products and node_modules
	rm -rf dist node_modules
