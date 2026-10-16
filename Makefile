# Build, test and run the monitor. See CLAUDE.md for the full picture.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Settings for `make dev` only. Override them on the command line,
# for example: make dev DEV_DATA_DIR=/tmp/monitor
DEV_DATA_DIR   ?= ./data
DEV_SECRET_KEY ?= dev-only-secret-not-for-production
DEV_BASE_URL   ?= http://localhost:8000

.PHONY: dev test e2e build web-build docker clean

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

# Everything CI checks: Go vet and race tests, web typecheck and Vitest.
test: web/node_modules
	go vet ./...
	go test -race ./...
	cd web && npm run typecheck && npm test

# Playwright end-to-end tests; they arrive with the Phase 1 dashboard.
e2e:
	@echo "No end-to-end tests yet."

# Build the dashboard into web/dist, which the Go binary embeds.
web-build: web/node_modules
	cd web && npm run build

# One binary with the dashboard inside: bin/monitor.
build: web-build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/monitor ./cmd/monitor

# Container image for this machine's platform.
docker:
	docker build --build-arg VERSION=$(VERSION) -t monitor:dev .

clean:
	rm -rf bin
	find web/dist -mindepth 1 ! -name .keep -exec rm -rf {} +
