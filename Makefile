# Build, test and run the monitor. See CLAUDE.md for the full picture.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Settings for `make dev` only. Override them on the command line,
# for example: make dev DEV_DATA_DIR=/tmp/monitor
DEV_DATA_DIR   ?= ./data
DEV_SECRET_KEY ?= dev-only-secret-not-for-production
DEV_BASE_URL   ?= http://localhost:8000

.PHONY: dev test test-postgres perf e2e build web-build docker docker-multiarch clean

# Install the web dependencies when package.json or the lockfile changes.
web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	@touch web/node_modules

# Go server on :8000 plus the Vite dev server (hot reload) on :5173, which
# forwards /ui/api and /health to Go. Ctrl-C stops both.
dev: web/node_modules
	go build -o bin/monitor-dev ./cmd/monitor
	@echo "Dashboard with hot reload: http://localhost:5173  (Go server: $(DEV_BASE_URL))"
	@DATA_DIR=$(DEV_DATA_DIR) SECRET_KEY=$(DEV_SECRET_KEY) BASE_URL=$(DEV_BASE_URL) LOG_LEVEL=debug \
		bin/monitor-dev serve & \
	server=$$!; trap 'kill $$server 2>/dev/null' EXIT; \
	cd web && npm run dev

# Everything CI checks: Go vet and race tests, web typecheck and Vitest, the
# fixture sanitizer's leak check, and that no committed fixture names this machine.
test: web/node_modules
	go vet ./...
	go test -race ./...
	cd web && npm run typecheck && npm test
	node --test internal/sentry/testdata/capture/sanitize.test.mjs tools/capture/leaks.test.mjs tools/capture/fixtures.test.mjs

# The tests that use the database (store, API, jobs and the others that
# open one) against Postgres 17: a throwaway container with its data in
# memory (no volume), removed when the tests end, also when they fail or
# are stopped. Each test gets a schema of its own (internal/store/storetest).
# The image is the official Debian one; POSTGRES_IMAGE=postgres:17-alpine
# runs them on Alpine (musl, which sorts text bytewise) instead.
POSTGRES_IMAGE ?= postgres:17
POSTGRES_TESTS ?= ./internal/store/... ./internal/api/ ./internal/jobs/ ./internal/ingest/ ./internal/auth/ ./internal/alerts/ \
	./internal/issues/ ./internal/sourcemaps/ ./internal/health/ ./internal/server/ ./cmd/...
test-postgres:
	@name=monitor-test-postgres-$$$$; \
	docker run --rm --detach --name $$name --tmpfs /var/lib/postgresql/data --publish 127.0.0.1::5432 \
		--env POSTGRES_USER=monitor --env POSTGRES_PASSWORD=monitor --env POSTGRES_DB=monitor \
		$(POSTGRES_IMAGE) -c fsync=off -c synchronous_commit=off -c full_page_writes=off >/dev/null || exit 1; \
	trap 'docker stop $$name >/dev/null' EXIT; trap 'exit 130' INT TERM; \
	for i in $$(seq 60); do docker exec $$name pg_isready --quiet --host 127.0.0.1 --username monitor && break; sleep 0.5; done; \
	docker exec $$name pg_isready --quiet --host 127.0.0.1 --username monitor || { echo "Postgres did not start"; exit 1; }; \
	port=$$(docker port $$name 5432/tcp | head -n 1 | sed 's/.*://'); \
	echo "Postgres ($(POSTGRES_IMAGE)) on 127.0.0.1:$$port"; \
	TEST_DATABASE_URL="postgres://monitor:monitor@127.0.0.1:$$port/monitor?sslmode=disable" go test -race $(POSTGRES_TESTS)

# The page statistics' performance check (docs/perf.md): 1 000 000 page
# views in a temp database, deleted afterwards.
perf:
	go test -tags perf ./internal/jobs -run TestPerf -v -timeout 30m

# Install the end-to-end test dependencies when their lockfile changes.
e2e/node_modules: e2e/package.json e2e/package-lock.json
	cd e2e && npm ci
	@touch e2e/node_modules

# Playwright end-to-end tests: the real binary, the Laravel and browser
# examples with the official Sentry SDKs, and the dashboard in Chromium.
# e2e/global-setup.ts builds the binary and starts everything.
e2e: web/node_modules e2e/node_modules
	cd e2e && npx playwright install chromium && npx playwright test

# Build the dashboard into web/dist, which the Go binary embeds.
web-build: web/node_modules
	cd web && npm run build

# One binary with the dashboard inside: bin/monitor.
build: web-build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/monitor ./cmd/monitor

# Container image for this machine's platform.
docker:
	docker build --build-arg VERSION=$(VERSION) -t monitor:dev .

# The image for every platform a release has, as CI builds it. The build
# stages cross-compile, so this needs no emulation. Without IMAGE_TAGS the
# result stays in the build cache (a check that every platform builds);
# with them it is pushed (CI on a version tag), for example
# IMAGE_TAGS="ghcr.io/owner/monitor:1.2.3 ghcr.io/owner/monitor:1".
PLATFORMS ?= linux/amd64,linux/arm64
IMAGE_TAGS ?=
docker-multiarch:
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		$(if $(strip $(IMAGE_TAGS)),$(foreach tag,$(IMAGE_TAGS),--tag $(tag)) --push,--output type=cacheonly) .

clean:
	rm -rf bin
	find web/dist -mindepth 1 ! -name .keep -exec rm -rf {} +
