package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"hooli.mail/server/internal/logger"
	"hooli.mail/server/internal/runtime"
)

func main() {
	logger.Init(logger.LevelInfo)

	cfg := parseFlags()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rt := runtime.New(cfg)
	if err := rt.Init(ctx); err != nil {
		logger.Error("init failed", "error", err)
		os.Exit(1)
	}
	defer rt.Close()

	if err := rt.Run(ctx); err != nil {
		os.Exit(1)
	}
}

func parseFlags() runtime.Config {
	dsn := flag.String("dsn", envOrDefault("DSN", "postgres://localhost:5432/hoolimail?sslmode=disable"), "PostgreSQL DSN")
	domain := flag.String("domain", envOrDefault("DOMAIN", ""), "Domain for Let's Encrypt TLS (e.g. mail.example.com)")
	acmeEmail := flag.String("acme-email", envOrDefault("ACME_EMAIL", ""), "Email for Let's Encrypt registration")
	smtpPort := flag.String("smtp-port", envOrDefault("SMTP_PORT", "2525"), "SMTP receiving port (plain)")
	submissionPort := flag.String("submission-port", envOrDefault("SUBMISSION_PORT", "587"), "SMTP submission port (STARTTLS)")
	imapPort := flag.String("imap-port", envOrDefault("IMAP_PORT", "143"), "IMAP port (STARTTLS)")
	imapsPort := flag.String("imaps-port", envOrDefault("IMAPS_PORT", "993"), "IMAPS port (TLS)")
	seedEmail := flag.String("seed", envOrDefault("SEED_EMAIL", ""), "Seed a user with this email address")
	seedPass := flag.String("seed-pass", envOrDefault("SEED_PASS", "password"), "Password for seed user")
	flag.Parse()

	return runtime.Config{
		DSN:            *dsn,
		Domain:         *domain,
		ACMEEmail:      *acmeEmail,
		SMTPPort:       *smtpPort,
		SubmissionPort: *submissionPort,
		IMAPPort:       *imapPort,
		IMAPSPort:      *imapsPort,
		SeedEmail:      *seedEmail,
		SeedPass:       *seedPass,
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
