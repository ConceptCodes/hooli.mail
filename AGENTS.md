# Repository Guidelines

## Project Structure & Module Organization

This repository contains the Hooli Mail server and terminal client. Executable entry points live in `cmd/server` and `cmd/tui`. Domain and infrastructure code is under `internal/`: authentication and configuration in `auth` and `config`, email parsing in `message`, protocol handlers in `server/smtp` and `server/imap`, storage implementations in `storage/memory` and `storage/postgres`, and Bubble Tea UI code in `tui`. Tests sit beside their packages as `*_test.go`. Container and task definitions are in `Dockerfile`, `docker-compose.yml`, and `justfile`.

## Build, Test, and Development Commands

- `just all` builds `bin/server` and `bin/tui`.
- `just ci` runs formatting checks, `go vet`, `golangci-lint`, and race-enabled tests; run it before opening a PR.
- `just test` runs `go test -race ./...`.
- `just lint` runs `golangci-lint` with the project's `.golangci.yml`; if the binary isn't on PATH it falls back to a pinned copy under `bin/` (install with `just install-golangci`).
- `just fmt` formats all Go files with `gofmt`.
- `docker compose up postgres -d` starts the local PostgreSQL 17 service.
- `go run ./cmd/server` or `go run ./cmd/tui` runs an entry point without producing a binary.
- `just deploy` builds and starts the full Compose stack; required server settings include `DOMAIN`.

Go 1.25 or newer is required. See `just --list` for additional cross-platform, Docker, and cleanup tasks.

## Coding Style & Naming Conventions

Follow idiomatic Go and let `gofmt` control tabs, spacing, and import grouping. Package names should be short and lowercase; exported identifiers use `PascalCase`, unexported identifiers use `camelCase`, and sentinel errors should use `ErrName`. Keep protocol logic in its existing package and place reusable data contracts in `internal/models` or `internal/mailstore`. Wrap errors with context using `%w` when callers need error-chain inspection.

## Testing Guidelines

Use the standard `testing` package and name tests `TestBehavior`, colocated with the implementation. Prefer table-driven tests for input variants. PostgreSQL integration tests require a disposable database through `TEST_POSTGRES_DSN`; without it, those tests skip. Do not parallelize shared-database migration tests. No numeric coverage threshold is enforced, but new behavior and bug fixes should include regression tests.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit subjects such as `fix(smtp): validate recipients` and `test(tui): add draft interaction tests`. Keep commits focused and atomic. PRs should explain the behavior change, link related issues, document configuration changes, and report `just ci` results. Include terminal screenshots for visible TUI changes.

## Security & Configuration

Never commit credentials, production DSNs, TLS certificates, or user mail. Use environment variables for server configuration and `hoolimail.config.example.json` as the client configuration reference.
