app := "hoolimail"
version := `git describe --tags --always 2>/dev/null || echo "dev"`
commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
goos := `go env GOOS`
goarch := `go env GOARCH`
golangci_version := "v2.12.2"

# Build both server and TUI
all: server tui

# Run all CI checks: format, vet, golangci-lint, race tests, image scan
ci: fmt-check vet lint test image-scan
    @echo "All CI checks passed."

# Format every Go file under the project
fmt:
    gofmt -w .

# Verify every file is gofmt-clean (CI gate)
fmt-check:
    gofmt -l . | tee /tmp/gofmt-issues.txt
    @test ! -s /tmp/gofmt-issues.txt || (echo "gofmt issues above"; rm -f /tmp/gofmt-issues.txt; exit 1)
    @rm -f /tmp/gofmt-issues.txt

# Run go vet on the whole module
vet:
    go vet ./...

# Run golangci-lint with the project's config (CI gate).
# Uses the locally installed binary if present; otherwise falls back to the
# pinned version installed by `just install-golangci`.
lint:
    @if command -v golangci-lint >/dev/null 2>&1; then \
        golangci-lint run ./...; \
    elif [ -x bin/golangci-lint-{{golangci_version}} ]; then \
        echo "golangci-lint not on PATH — using pinned {{golangci_version}} under bin/"; \
        bin/golangci-lint-{{golangci_version}} run ./...; \
    else \
        echo "golangci-lint not found. Install with: just install-golangci"; \
        exit 1; \
    fi

# Install the pinned golangci-lint binary locally (no sudo needed).
# Downloads a pre-built release into bin/ so a fresh checkout can run `just ci`
# without root or a Go toolchain for the linter.
install-golangci:
    @mkdir -p bin
    @if [ -x bin/golangci-lint-{{golangci_version}} ]; then \
        echo "bin/golangci-lint-{{golangci_version}} already installed"; \
    else \
        ver="$(echo {{golangci_version}} | sed 's/^v//')"; \
        case "$(uname -s)" in \
            Darwin) os=darwin ;; \
            Linux)  os=linux  ;; \
            *) echo "unsupported OS for golangci-lint install"; exit 1 ;; \
        esac; \
        case "$(uname -m)" in \
            arm64|aarch64) arch=arm64 ;; \
            x86_64|amd64)  arch=amd64 ;; \
            *) echo "unsupported arch for golangci-lint install"; exit 1 ;; \
        esac; \
        url="https://github.com/golangci/golangci-lint/releases/download/{{golangci_version}}/golangci-lint-$ver-$os-$arch.tar.gz"; \
        echo "downloading $url"; \
        curl -fsSL "$url" | tar -xz -C bin --strip-components=1 "golangci-lint-$ver-$os-$arch/golangci-lint"; \
        mv bin/golangci-lint bin/golangci-lint-{{golangci_version}}; \
        echo "installed bin/golangci-lint-{{golangci_version}}"; \
    fi

# Run tests with the race detector
test:
    go test -race ./...

# Run tests including postgres integration (requires TEST_POSTGRES_DSN)
test-integration:
    go test -race ./...

# Build the mail server
server:
    @echo "Building server for {{goos}}/{{goarch}}..."
    @mkdir -p bin
    CGO_ENABLED=0 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/server ./cmd/server

# Build the TUI
tui:
    @echo "Building TUI for {{goos}}/{{goarch}}..."
    @mkdir -p bin
    CGO_ENABLED=0 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/tui ./cmd/tui

# Build TUI for all platforms (linux/darwin/windows, amd64/arm64)
build-tui-all:
    @echo "Building TUI for all platforms..."
    @mkdir -p bin
    CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/tui-linux-amd64   ./cmd/tui
    CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/tui-linux-arm64   ./cmd/tui
    CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/tui-darwin-amd64  ./cmd/tui
    CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/tui-darwin-arm64  ./cmd/tui
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/tui-windows-amd64.exe ./cmd/tui
    @echo "Build complete. Files in bin/:"
    @ls -lh bin/

# Build Docker image
docker-build:
    docker build -t {{app}}:{{version}} .
    docker tag {{app}}:{{version}} {{app}}:latest

# Push Docker image (requires DOCKER_REGISTRY env)
docker-push:
    @echo "Set DOCKER_REGISTRY and push manually"
    @echo "  docker tag {{app}} \$DOCKER_REGISTRY/{{app}}:{{version}}"
    @echo "  docker push \$DOCKER_REGISTRY/{{app}}:{{version}}"

# Deploy with docker-compose
deploy:
    @echo "Deploying with docker-compose..."
    @cp -n .env.example .env 2>/dev/null || true
    docker compose up -d --build

# Tail docker-compose logs
logs:
    docker compose logs -f

# Stop docker-compose services
stop:
    docker compose down

# Scan the Docker image for HIGH/CRITICAL vulnerabilities (requires trivy)
image-scan:
    docker build -t {{app}}:scan .
    trivy image --severity HIGH,CRITICAL --exit-code 1 {{app}}:scan

# Clean build artifacts
clean:
    rm -rf bin/
    go clean
