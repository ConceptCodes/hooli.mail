package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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
	var acmeMgr *autocert.Manager

	if *domain != "" {
		log.Printf("Domain: %s — provisioning Let's Encrypt certificates", *domain)

		acmeMgr = &autocert.Manager{
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
			acmeMgr.Email = *acmeEmail
		}

		tlsConfig = &tls.Config{
			GetCertificate:           acmeMgr.GetCertificate,
			MinVersion:               tls.VersionTLS12,
			PreferServerCipherSuites: true,
		}
	} else {
		log.Println("No domain set — running without TLS (dev mode)")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	authn := auth.NewAuthenticator(store)

	smtpReceiving := smtp.NewServer(ctx, store, authn, ":"+*smtpPort, nil, false)
	smtpSubmission := smtp.NewServer(ctx, store, authn, ":"+*submissionPort, tlsConfig, true)
	imapStartTLS := imap.NewServer(ctx, store, authn, ":"+*imapPort, tlsConfig, false)
	imapTLS := imap.NewServer(ctx, store, authn, ":"+*imapsPort, tlsConfig, true)

	starters := []struct {
		Name string
		Run  func() error
	}{
		{"SMTP receiving :" + *smtpPort, smtpReceiving.Start},
		{"SMTP submission :" + *submissionPort, smtpSubmission.Start},
		{"IMAP :" + *imapPort, imapStartTLS.Start},
		{"IMAPS :" + *imapsPort, imapTLS.Start},
	}

	// errCh collects fatal errors from goroutines without ever calling
	// os.Exit from a goroutine (which would skip deferred cleanup).
	errCh := make(chan error, len(starters)+1)
	var wg sync.WaitGroup

	// shutdown holds servers that need an explicit Close/Shutdown during
	// teardown. The ACME HTTP server is retained here so we can drain its
	// in-flight requests instead of killing them on process exit.
	shutdown := struct {
		acme *http.Server
	}{}

	run := func(name string, fn func() error) {
		defer wg.Done()
		log.Printf("Starting %s...", name)
		if err := fn(); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			errCh <- fmt.Errorf("%s: %w", name, err)
		}
	}

	if tlsConfig != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Println("Starting ACME HTTP challenge server on :80")
			// Retain the *http.Server so we can Shutdown it cleanly during
			// teardown rather than letting in-flight ACME challenge requests
			// get killed mid-flight. ListenAndServe always returns a non-nil
			// error; http.ErrServerClosed / net.ErrClosed mean we shut it down
			// ourselves and aren't fatal.
			acmeSrv := &http.Server{
				Addr:     ":80",
				Handler:  acmeMgr.HTTPHandler(nil),
				ErrorLog: log.Default(),
			}
			shutdown.acme = acmeSrv
			if err := acmeSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				errCh <- fmt.Errorf("ACME HTTP server: %w", err)
			}
		}()
	}

	for _, s := range starters {
		wg.Add(1)
		go run(s.Name, s.Run)
	}

	// Wait for either a signal or a fatal server error. Either way, we then
	// tear everything down and let deferred store.Close() run on the way out.
	var shutdownErr error
	select {
	case <-sigCh:
		log.Println("Shutting down...")
	case err := <-errCh:
		log.Printf("Fatal: %v", err)
		shutdownErr = err
	}

	smtpReceiving.Stop()
	smtpSubmission.Stop()
	imapStartTLS.Stop()
	imapTLS.Stop()
	if shutdown.acme != nil {
		// 5s is enough for an ACME challenge round-trip; we don't want a
		// wedged connection to delay process exit.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := shutdown.acme.Shutdown(shutdownCtx); err != nil {
			log.Printf("ACME HTTP shutdown: %v", err)
		}
		shutdownCancel()
	}
	cancel()

	// Give in-flight goroutines a chance to return, then bail. We don't wait
	// forever — protocol sessions stuck mid-DB-call will be cancelled by the
	// context already, so a brief wait is enough.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Println("Shutdown: some goroutines did not exit within 5s")
	}

	if shutdownErr != nil {
		os.Exit(1)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
