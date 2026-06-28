app := "hoolimail"
version := `git describe --tags --always 2>/dev/null || echo "dev"`
commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
goos := `go env GOOS`
goarch := `go env GOARCH`

# Build both server and TUI
all: server tui

# Build the mail server
server:
    @echo "Building server for {{goos}}/{{goarch}}..."
    CGO_ENABLED=0 go build -ldflags='-s -w -X main.version={{version}} -X main.commit={{commit}}' -o bin/server ./cmd/server

# Build the TUI
tui:
    @echo "Building TUI for {{goos}}/{{goarch}}..."
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

# Clean build artifacts
clean:
    rm -rf bin/
    go clean
