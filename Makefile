GO ?= go
NPM ?= npm
BINARY ?= dist/synon-go

.PHONY: test build dev-backend dev-frontend source-quickstart source-quickstart-test source-cli-install source-cli-test source-backend-watch-test source-frontend-host-test frontend-install frontend-typecheck frontend-lint frontend-test frontend-build frontend-test-packaged frontend-audit smoke smoke-http smoke-live-im smoke-v11-chembl-admet stress-http benchmark-runtime benchmark-runtime-test external-acceptance-test package package-test package-windows-test install-test release-lifecycle-test release-p8-test conda-runtime-lock-test supply-chain-test release-candidate-contract-test clean-copy-build-test clean goal-run goal-run-gate-test goal-run-strict-config-test audit-agent-runtime-env-test audit-agent-runtime-full-gates-test audit-agent-runtime-quick audit-agent-runtime-full audit-source-clean audit-source-clean-test audit-non-web-boundary audit-non-web-boundary-test env-example-test ci-contract-test

test:
	$(GO) test ./...

build:
	mkdir -p dist
	$(GO) build -buildvcs=false -o $(BINARY) ./cmd/synon

dev-backend:
	GO=$(GO) bash scripts/dev/source-backend-watch.sh

dev-frontend:
	SYNON_DEV_WEB_PORT=$${SYNON_DEV_WEB_PORT:-8765} SYNON_DEV_BACKEND_URL=$${SYNON_DEV_BACKEND_URL:-http://127.0.0.1:8766} NPM=$(NPM) bash scripts/dev/source-frontend-host.sh

source-quickstart:
	bash scripts/dev/source-quickstart.sh

source-quickstart-test:
	bash scripts/dev/source-quickstart-test.sh

source-cli-install:
	bash scripts/dev/install-source-cli.sh

source-cli-test:
	bash scripts/dev/source-cli-test.sh

source-backend-watch-test:
	bash scripts/dev/source-backend-watch-test.sh

source-frontend-host-test:
	bash scripts/dev/source-frontend-host-test.sh

frontend-install:
	cd frontend && $(NPM) ci --ignore-scripts

frontend-typecheck:
	cd frontend && $(NPM) run typecheck

frontend-lint:
	cd frontend && $(NPM) run lint

frontend-test:
	cd frontend && $(NPM) run test

frontend-build:
	cd frontend && NODE_OPTIONS="$${NODE_OPTIONS:---max-old-space-size=4096}" $(NPM) run build

frontend-test-packaged:
	cd frontend && $(NPM) run test:packaged

frontend-audit:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/audit/audit_frontend_migration.py --root . --check

smoke: build
	$(BINARY) --version
	$(BINARY) --health-json

smoke-http: build
	bash scripts/smoke-http.sh $(BINARY)

smoke-live-im:
	$(GO) run ./scripts/smoke-live-im

smoke-v11-chembl-admet:
	python3 scripts/compat/check-v11-chembl-admet.py --live

stress-http: build
	bash scripts/stress-http.sh $(BINARY)

benchmark-runtime: build
	python3 scripts/benchmark-runtime.py --go-binary $(BINARY)

benchmark-runtime-test:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/benchmark-runtime-test.py

external-acceptance-test:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/p9_external_acceptance_test.py

package:
	GO=$(GO) bash scripts/package-release.sh release

package-test:
	GO=$(GO) bash scripts/package-release-test.sh

package-windows-test:
	GO=$(GO) bash scripts/package-windows-release-test.sh release-windows

install-test:
	GO=$(GO) bash scripts/install-release-test.sh

release-lifecycle-test:
	GO=$(GO) bash scripts/release-lifecycle-test.sh

release-p8-test:
	GO=$(GO) bash scripts/release-p8-gate.sh

conda-runtime-lock-test:
	node --test scripts/generate-conda-runtime-lock.test.mjs

supply-chain-test: conda-runtime-lock-test release-candidate-contract-test
	$(GO) test ./internal/releasepkg -run '^TestGenerateAndVerifySupplyChainForRealSynonBinary$$' -count=1
	bash scripts/source-tree-digest-test.sh

release-candidate-contract-test:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest \
		scripts.quality.test_release_candidate_manifest \
		scripts.quality.test_release_receipt_gate

clean-copy-build-test:
	GO=$(GO) bash scripts/clean-copy-build-test.sh

goal-run:
	@if [ -z "$${SYNON_BINARY}" ]; then \
		SYNON_BINARY=$(BINARY); \
	else \
		SYNON_BINARY=$${SYNON_BINARY}; \
	fi; \
	if [ ! -x "$$SYNON_BINARY" ]; then \
		echo "SYNON_BINARY not found or not executable: $$SYNON_BINARY"; \
		exit 1; \
	fi; \
	bash scripts/goal-run.sh $$SYNON_BINARY

goal-run-gate-test:
	bash scripts/goal-run-doctor-gate-test.sh

goal-run-strict-config-test:
	GO=$(GO) bash scripts/goal-run-strict-config-test.sh

audit-agent-runtime-env-test:
	bash scripts/audit/audit-agent-runtime-env-test.sh

audit-agent-runtime-full-gates-test:
	bash scripts/audit/audit-agent-runtime-full-gates-test.sh

audit-agent-runtime-quick:
	bash scripts/audit/audit-agent-runtime.sh --quick

audit-agent-runtime-full:
	bash scripts/audit/audit-agent-runtime.sh --full

audit-source-clean:
	bash scripts/audit/audit-source-clean.sh

audit-source-clean-test:
	bash scripts/audit/audit-source-clean-test.sh

audit-non-web-boundary:
	bash scripts/audit/audit-non-web-boundary.sh

audit-non-web-boundary-test:
	bash scripts/audit/audit-non-web-boundary-test.sh

env-example-test:
	bash scripts/env-example-test.sh

ci-contract-test:
	bash scripts/ci-contract-test.sh

clean:
	rm -rf dist release release-windows coverage.out frontend/out frontend/coverage frontend/test-results
