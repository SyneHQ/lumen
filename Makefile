.PHONY: all proto build run test docker-up docker-down clean

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
	./bin/lumen

test:
	@echo "Running Go unit tests..."
	go test -v ./...

docker-up:
	docker compose up -d

docker-down:
	docker compose down

clean:
	rm -rf bin/ gen/
