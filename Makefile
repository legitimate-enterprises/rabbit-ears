# CI runs these same targets. Each stack in STACKS owns a Makefile with the
# same target names.

STACKS := server web

.DEFAULT_GOAL := help
.PHONY: help deps generate build run test test-unit test-integration test-e2e style lint format clean release fixtures

help: ## Print this target list
	@grep -hE '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) \
		| sort -t: -k1,1 \
		| awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

deps: ## Install every stack's tooling, fetch fixtures
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s deps || exit $$?; done
	$(MAKE) --no-print-directory fixtures

generate: ## Regenerate API code in every stack
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s generate || exit $$?; done

fixtures: ## Fetch or synthesize fixtures/ (FIXTURES_SOURCE=dir copies instead)
	scripts/fetch-fixtures.sh

build: ## Compile check in every stack
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s build || exit $$?; done

run: ## Run server and web dev server
	$(MAKE) --no-print-directory -C server run & \
	$(MAKE) --no-print-directory -C web run; wait

# Fastest first; exits with the last failure's status. CI runs each group
# as one job per stack instead.
TEST_GROUPS := style lint test-unit test-integration test-e2e build

SEPARATOR := ========================================================================

test: ## Every gate CI enforces; every group runs even when one fails
	@status=0; \
	for group in $(TEST_GROUPS); do \
		printf '\n%s\n' "$(SEPARATOR)"; \
		printf 'TEST: %s\n' "$$group"; \
		printf '%s\n' "$(SEPARATOR)"; \
		if $(MAKE) --no-print-directory "$$group"; then \
			printf 'PASS: %s\n' "$$group"; \
		else \
			group_status=$$?; \
			status=$$group_status; \
			printf 'FAIL: %s (exit %s)\n' "$$group" "$$group_status"; \
		fi; \
	done; \
	printf '\n'; \
	if [ "$$status" -eq 0 ]; then printf 'all %s groups passed\n' "$(words $(TEST_GROUPS))"; \
	else printf 'at least one group failed; see FAIL lines above\n'; fi; \
	exit $$status

test-unit: ## Unit suites in every stack
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s test-unit || exit $$?; done

test-integration: ## The server against ffmpeg and fixtures/
	$(MAKE) --no-print-directory -C server test-integration

test-e2e: ## Browser flows against a real server
	$(MAKE) --no-print-directory -C web test-e2e

style: ## Formatter checks in every stack
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s style || exit $$?; done

format: ## Formatters in place (the fix for `make style`)
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s format 2>/dev/null || true; done

lint: ## Linters in every stack
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s lint || exit $$?; done

clean: ## Remove build products and fixtures
	@for s in $(STACKS); do $(MAKE) --no-print-directory -C $$s clean || exit $$?; done
	rm -rf fixtures

# There is no local publish path.
release: ## Tag origin/main v<version> once CI is green there, and push the tag
	scripts/cut-release.sh --yes
