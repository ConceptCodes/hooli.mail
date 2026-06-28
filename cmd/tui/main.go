package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"hooli.mail/server/internal/config"
	"hooli.mail/server/internal/tui"

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

	m := tui.New(*server, *insecure, cfg)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}

	fmt.Println("Goodbye!")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
