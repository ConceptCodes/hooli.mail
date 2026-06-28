APP = hoolimail
SERVER_BIN = server
TUI_BIN = tui
VERSION = $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT = $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE = $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

GOOS = $(shell go env GOOS)
GOARCH = $(shell go env GOARCH)

.PHONY: all server tui docker-build docker-push clean build-tui-all

all: server tui

server:
	@echo "Building server for $(GOOS)/$(GOARCH)..."
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(SERVER_BIN) ./cmd/server

tui:
	@echo "Building TUI for $(GOOS)/$(GOARCH)..."
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(TUI_BIN) ./cmd/tui

build-tui-all:
	@echo "Building TUI for all platforms..."
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o bin/$(TUI_BIN)-linux-amd64   ./cmd/tui
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o bin/$(TUI_BIN)-linux-arm64   ./cmd/tui
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o bin/$(TUI_BIN)-darwin-amd64  ./cmd/tui
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o bin/$(TUI_BIN)-darwin-arm64  ./cmd/tui
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(TUI_BIN)-windows-amd64.exe ./cmd/tui
	@echo "Build complete. Files in bin/:"
	@ls -lh bin/

docker-build:
	docker build -t $(APP):$(VERSION) .
	docker tag $(APP):$(VERSION) $(APP):latest

docker-push:
	@echo "Set DOCKER_REGISTRY and push manually"
	@echo "  docker tag $(APP) \$$DOCKER_REGISTRY/$(APP):$(VERSION)"
	@echo "  docker push \$$DOCKER_REGISTRY/$(APP):$(VERSION)"

deploy:
	@echo "Deploying with docker-compose..."
	@cp -n .env.example .env 2>/dev/null || true
	docker compose up -d --build

logs:
	docker compose logs -f

stop:
	docker compose down

clean:
	rm -rf bin/
	go clean

.PHONY: server tui build-tui-all docker-build docker-push deploy logs stop clean
