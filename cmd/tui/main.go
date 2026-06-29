package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"hooli.mail/server/internal/config"
	"hooli.mail/server/internal/tui"
	"hooli.mail/server/internal/tui/mail"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	cfg, err := config.Ensure()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	defaultServer := cfg.Server
	if defaultServer == "" {
		defaultServer = "localhost"
	}

	server := flag.String("server", envOrDefault("MAIL_SERVER", defaultServer), "Mail server hostname")
	insecure := flag.Bool("insecure", cfg.Insecure, "Use plain IMAP (no TLS)")
	flag.Parse()

	// Build the session outside the model so cmd/tui owns it and can LOGOUT
	// after the Bubble Tea program returns — the message loop drops in-flight
	// commands on tea.Quit, so logout must be synchronous from here.
	session := mail.NewIMAPSession(*server, *insecure)
	m := tui.NewWithSession(session, *server, *insecure, cfg)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}

	// Best-effort logout with a short cap so a wedged server can't hang the
	// process on exit. Errors aren't fatal — the user already chose to quit.
	logoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Logout(logoutCtx); err != nil {
		fmt.Fprintf(os.Stderr, "logout: %v\n", err)
	}

	fmt.Println("Goodbye!")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
