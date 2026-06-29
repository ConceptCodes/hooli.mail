# Hooli Mail

A full-featured mail server written in Go with built-in TUI (Terminal User Interface) client, supporting SMTP, IMAP, and modern email protocols.

## Features

- **Full Mail Server**: SMTP, IMAP, and IMAPS protocol support
- **Multi-User Support**: User authentication and account management
- **TUI Client**: Beautiful terminal-based email client built with Charm/Bubbletea
- **PostgreSQL Storage**: Robust email storage and message indexing
- **Docker Ready**: Pre-configured Docker Compose setup for easy deployment
- **Let's Encrypt Integration**: Automatic TLS certificate management
- **Customizable Theme**: Dark and light themes with configurable colors
- **Message Management**: Full email lifecycle management (send, receive, delete, search)

## Quick Start

### Prerequisites

- Docker & Docker Compose (recommended)
- Or: Go 1.25+ and PostgreSQL 17+

### Using Docker Compose (Recommended)

1. **Clone and configure**:
   ```bash
   git clone <repository-url>
   cd mail-server
   cp .env.example .env
   ```

2. **Set your domain and optional seed user**:
   ```bash
   # Edit .env with your settings
   DOMAIN=mail.example.com
   ACME_EMAIL=admin@example.com
   SEED_EMAIL=admin@example.com
   SEED_PASS=secure-password
   ```

3. **Start the server**:
   ```bash
   docker compose up
   ```

The server will:
- Automatically provision TLS certificates via Let's Encrypt
- Initialize the PostgreSQL database
- Start listening on standard ports (80, 143, 587, 993, 2525)

### Ports

- **80**: HTTP (Let's Encrypt validation)
- **143**: IMAP (unencrypted)
- **587**: SMTP Submission (TLS)
- **993**: IMAPS (encrypted IMAP)
- **2525**: SMTP (unencrypted, for testing)

### Local Development

1. **Start PostgreSQL**:
   ```bash
   docker compose up postgres -d
   ```

2. **Build and run the server**:
   ```bash
   go build -o bin/server ./cmd/server
   ./bin/server
   ```

3. **Build and run the TUI client**:
   ```bash
   go build -o bin/tui ./cmd/tui
   ./bin/tui
   ```

## Project Structure

```
.
├── cmd/
│   ├── server/          # Mail server entry point
│   └── tui/             # Terminal UI client entry point
├── internal/
│   ├── auth/            # Authentication & user management
│   ├── config/          # Configuration management
│   ├── mailstore/       # Email storage layer
│   ├── message/         # Email message handling
│   ├── models/          # Database models
│   ├── server/          # SMTP/IMAP protocol handlers
│   ├── storage/         # Database abstraction
│   └── tui/             # Terminal UI components
├── Dockerfile           # Container build specification
├── docker-compose.yml   # Multi-container orchestration
├── go.mod / go.sum      # Go dependency management
└── README.md           # This file
```

## Configuration

The server is configured via environment variables:

| Variable | Required | Description | Default |
|----------|----------|-------------|---------|
| `DOMAIN` | Yes | Mail server hostname for TLS | - |
| `ACME_EMAIL` | No | Let's Encrypt notification email | `admin@{DOMAIN}` |
| `DSN` | No | PostgreSQL connection string | `postgres://hooli:hooli@localhost:5432/hoolimail?sslmode=disable` |
| `SEED_EMAIL` | No | Admin email for first-run setup | - |
| `SEED_PASS` | No | Admin password for first-run setup | - |
| `SMTP_PORT` | No | SMTP service port | `2525` |
| `SUBMISSION_PORT` | No | SMTP submission port (587) | `587` |
| `IMAP_PORT` | No | IMAP service port | `143` |
| `IMAPS_PORT` | No | Secure IMAP port | `993` |

### Client Configuration

Create `~/.hoolimail/hoolimail.config.json` to customize the TUI client:

```json
{
  "theme": {
    "dark": {
      "ink":   "#ebebeb",
      "dim":   "#999999",
      "faint": "#555555",
      "seal":  "#e58e3c",
      "error": "#e04f5f"
    },
    "light": {
      "ink":   "#1a1a1a",
      "dim":   "#555555",
      "faint": "#999999",
      "seal":  "#c05a10",
      "error": "#b91c1c"
    }
  },
  "server":       "mail.example.com",
  "insecure":     false,
  "date_format":  "relative",
  "signature":    "\n--\nSent from Hooli Mail",
  "poll_seconds": 30,
  "page_size":    50
}
```

## Development

### Building Locally

```bash
# Server
go build -o bin/server ./cmd/server

# TUI Client
go build -o bin/tui ./cmd/tui
```

### Running Tests

```bash
go test ./...
```

### Code Organization

- **auth**: User authentication, password hashing, session management
- **config**: Server configuration and environment variable parsing
- **mailstore**: High-level email operations (send, receive, search, delete)
- **message**: Email message parsing and RFC 5322 compliance
- **models**: Data structures for users, messages, mailboxes
- **server**: Protocol handlers (SMTP, IMAP, IMAPS)
- **storage**: Database abstraction layer (PostgreSQL)
- **tui**: Bubbletea-based terminal interface components

## Technology Stack

- **Language**: Go 1.25+
- **Database**: PostgreSQL 17+
- **Email Protocols**: IMAP, SMTP, SASL
- **TLS/Security**: crypto/tls, golang.org/x/crypto
- **UI Framework**: Charm (Bubbletea, Lipgloss, Glamour)
- **Database Driver**: pgx v5

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to get started.