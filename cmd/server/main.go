package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/acme/autocert"

	"hooli.mail/server/internal/auth"
	"hooli.mail/server/internal/server/imap"
	"hooli.mail/server/internal/server/smtp"
	"hooli.mail/server/internal/storage/postgres"
)

func main() {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := postgres.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()

	if *seedEmail != "" {
		existing, err := store.GetUserByEmail(ctx, *seedEmail)
		if err != nil {
			log.Fatalf("check user: %v", err)
		}
		if existing != nil {
			log.Printf("User %s already exists, skipping seed", *seedEmail)
		} else {
			hash, err := auth.HashPassword(*seedPass)
			if err != nil {
				log.Fatalf("hash password: %v", err)
			}
			user, err := store.CreateUser(ctx, *seedEmail, hash)
			if err != nil {
				log.Fatalf("create user: %v", err)
			}
			log.Printf("Seeded user %s (id=%d)", user.Email, user.ID)
		}
	}

	var tlsConfig *tls.Config

	if *domain != "" {
		log.Printf("Domain: %s — provisioning Let's Encrypt certificates", *domain)

		m := &autocert.Manager{
			Cache:  autocert.DirCache("certs"),
			Prompt: autocert.AcceptTOS,
			HostPolicy: func(_ context.Context, host string) error {
				if host == *domain {
					return nil
				}
				return fmt.Errorf("unexpected host %q", host)
			},
		}
		if *acmeEmail != "" {
			m.Email = *acmeEmail
		}

		go func() {
			log.Println("Starting ACME HTTP challenge server on :80")
			if err := http.ListenAndServe(":80", m.HTTPHandler(nil)); err != nil {
				log.Fatalf("ACME HTTP server: %v", err)
			}
		}()

		tlsConfig = &tls.Config{
			GetCertificate:           m.GetCertificate,
			MinVersion:               tls.VersionTLS12,
			PreferServerCipherSuites: true,
		}
	} else {
		log.Println("No domain set — running without TLS")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	authn := auth.NewAuthenticator(store)

	smtpReceiving := smtp.NewServer(store, authn, ":"+*smtpPort, nil, false)
	smtpSubmission := smtp.NewServer(store, authn, ":"+*submissionPort, tlsConfig, true)
	imapStartTLS := imap.NewServer(store, authn, ":"+*imapPort, tlsConfig)
	imapTLS := imap.NewServer(store, authn, ":"+*imapsPort, tlsConfig)

	start := []struct {
		Name string
		Run  func() error
	}{
		{"SMTP receiving :" + *smtpPort, smtpReceiving.Start},
		{"SMTP submission :" + *submissionPort, smtpSubmission.Start},
		{"IMAP :" + *imapPort, imapStartTLS.Start},
		{"IMAPS :" + *imapsPort, imapTLS.Start},
	}

	for _, s := range start {
		s := s
		go func() {
			log.Printf("Starting %s...", s.Name)
			if err := s.Run(); err != nil {
				if isClosedNetErr(err) {
					return
				}
				log.Fatalf("%s: %v", s.Name, err)
			}
		}()
	}

	<-sigCh
	log.Println("Shutting down...")
	smtpReceiving.Stop()
	smtpSubmission.Stop()
	imapStartTLS.Stop()
	imapTLS.Stop()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func isClosedNetErr(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(*net.OpError); ok {
		return ne.Err.Error() == "use of closed network connection"
	}
	return false
}
