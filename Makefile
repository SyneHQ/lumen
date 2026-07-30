.PHONY: all proto build run test test-race lint license-check boundary-check \
        verify enterprise-build docker-up docker-down clean help

PATH := $(PATH):$(shell go env GOPATH)/bin

all: proto build test

proto:
	@echo "Generating Protobuf descriptors & Connect Go code..."
	mkdir -p gen
	protoc --proto_path=. \
		--go_out=gen --go_opt=module=github.com/SyneHQ/lumen/gen \
		--connect-go_out=gen --connect-go_opt=module=github.com/SyneHQ/lumen/gen \
		proto/lumen/v1/ingest.proto proto/lumen/v1/admin.proto

build:
	@echo "Building lumen binary..."
	go build -o bin/lumen ./cmd/lumen

run: build
	@echo "Starting lumen service..."
	@test -n "$$ADMIN_TOKEN" || { \
		echo "ADMIN_TOKEN is not set. It has no default."; \
		echo "  export ADMIN_TOKEN=\"\$$(openssl rand -hex 32)\""; \
		echo "or set LUMEN_DEV=1 for a throwaway local token."; \
		test -n "$$LUMEN_DEV"; }
	./bin/lumen

test:
	@echo "Running Go unit tests..."
	go test -v ./...

test-race:
	@echo "Running Go tests with the data race detector..."
	go test -race ./...

lint:
	@echo "Checking formatting and running vet..."
	@out=$$(gofmt -l . | grep -v '^enterprise/' || true); \
	 if [ -n "$$out" ]; then echo "gofmt violations:"; echo "$$out"; exit 1; fi
	go vet ./...

license-check:
	@echo "Verifying licensing metadata..."
	@for f in LICENSE NOTICE sdk/LICENSE sdk/NOTICE OPEN_CORE.md SECURITY.md \
	          CONTRIBUTING.md CODE_OF_CONDUCT.md TRADEMARK.md; do \
		test -s "$$f" || { echo "missing or empty: $$f"; exit 1; }; \
	done
	@grep -q "GNU AFFERO GENERAL PUBLIC LICENSE" LICENSE || { echo "LICENSE is not AGPL-3.0"; exit 1; }
	@grep -q "Apache License" sdk/LICENSE || { echo "sdk/LICENSE is not Apache-2.0"; exit 1; }
	@echo "OK"

# Proves the open-source build never needs the private module.
boundary-check:
	@echo "Verifying open-core boundary..."
	@grep -q "lumen-enterprise" go.mod go.sum \
		&& { echo "public go.mod/go.sum references the private module"; exit 1; } \
		|| echo "  go.mod clean"
	@! grep -rqn --include='*.go' -E '^//go:build.*\bee\b' . \
		|| { echo "found an 'ee' build tag; the boundary uses ee.Hooks injection"; exit 1; }
	@echo "  no build tags"
	go build ./...
	@echo "OK"

verify: lint license-check boundary-check test-race
	@echo "All checks passed."

# Builds the commercial binary. Requires the private submodule.
enterprise-build:
	@test -f enterprise/go.mod || { \
		echo "enterprise/ not present. Run: git submodule update --init --recursive"; \
		echo "(or scripts/setup-enterprise.sh for first-time setup)"; exit 1; }
	cd enterprise && go build -o ../bin/lumen-enterprise ./cmd/lumen-enterprise

docker-up:
	docker compose up -d

docker-down:
	docker compose down

clean:
	rm -rf bin/ gen/

help:
	@echo "Targets:"
	@echo "  build             build the community binary into bin/lumen"
	@echo "  run               build and run (needs ADMIN_TOKEN or LUMEN_DEV=1)"
	@echo "  test / test-race  run tests"
	@echo "  lint              gofmt + go vet"
	@echo "  license-check     assert license files and metadata are present"
	@echo "  boundary-check    assert the open build needs no private code"
	@echo "  verify            everything above; run before pushing"
	@echo "  enterprise-build  build the commercial binary (private submodule)"
	@echo "  proto             regenerate protobuf/Connect code"
