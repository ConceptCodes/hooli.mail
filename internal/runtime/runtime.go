// Package runtime owns the mail server lifecycle: storage initialization,
// TLS provisioning, protocol server startup, supervised execution, and
// coordinated shutdown. Extracting this from main gives the lifecycle a
// module with locality and a seam for tests: a test can construct a Runtime,
// cancel its context, and verify that servers stop and resources are
// released — none of which is possible when the logic lives in an untested
// main function.
package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"hooli.mail/server/internal/auth"
	"hooli.mail/server/internal/server/imap"
	"hooli.mail/server/internal/server/smtp"
	"hooli.mail/server/internal/storage/postgres"
)

// Config holds the settings the runtime needs to start servers.
type Config struct {
	DSN            string
	Domain         string // empty = no TLS (dev mode)
	ACMEEmail      string
	SMTPPort       string
	SubmissionPort string
	IMAPPort       string
	IMAPSPort      string
	SeedEmail      string // empty = no seed
	SeedPass       string
}

// Runtime owns the full mail server lifecycle. It is constructed from a
// Config, initialized (storage + TLS), run (servers + supervision), and
// closed (resource release). Each phase is a separate method so tests can
// exercise them independently.
type Runtime struct {
	cfg       Config
	store     *postgres.Store
	authn     *auth.Authenticator
	tlsConfig *tls.Config
	acmeMgr   *autocert.Manager

	smtpReceiving  *smtp.Server
	smtpSubmission *smtp.Server
	imapStartTLS   *imap.Server
	imapTLS        *imap.Server
	acmeHTTP       *http.Server
}

func New(cfg Config) *Runtime {
	return &Runtime{cfg: cfg}
}

// Init connects to the database, seeds the initial user if requested, and
// provisions TLS/ACME. It must be called before Run.
func (r *Runtime) Init(ctx context.Context) error {
	store, err := postgres.New(ctx, r.cfg.DSN)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	r.store = store

	if r.cfg.SeedEmail != "" {
		if err := r.seed(ctx); err != nil {
			return err
		}
	}

	r.provisionTLS()
	r.authn = auth.NewAuthenticator(r.store)
	return nil
}

func (r *Runtime) seed(ctx context.Context) error {
	existing, err := r.store.GetUserByEmail(ctx, r.cfg.SeedEmail)
	if err != nil {
		return fmt.Errorf("check user: %w", err)
	}
	if existing != nil {
		log.Printf("User %s already exists, skipping seed", r.cfg.SeedEmail)
		return nil
	}
	hash, err := auth.HashPassword(r.cfg.SeedPass)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	user, err := r.store.CreateUser(ctx, r.cfg.SeedEmail, hash)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	log.Printf("Seeded user %s (id=%d)", user.Email, user.ID)
	return nil
}

func (r *Runtime) provisionTLS() {
	if r.cfg.Domain == "" {
		log.Println("No domain set — running without TLS (dev mode)")
		return
	}

	log.Printf("Domain: %s — provisioning Let's Encrypt certificates", r.cfg.Domain)
	r.acmeMgr = &autocert.Manager{
		Cache:  autocert.DirCache("certs"),
		Prompt: autocert.AcceptTOS,
		HostPolicy: func(_ context.Context, host string) error {
			if host == r.cfg.Domain {
				return nil
			}
			return fmt.Errorf("unexpected host %q", host)
		},
	}
	if r.cfg.ACMEEmail != "" {
		r.acmeMgr.Email = r.cfg.ACMEEmail
	}
	r.tlsConfig = &tls.Config{
		GetCertificate:           r.acmeMgr.GetCertificate,
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
	}
}

// Run starts all protocol servers, blocks until ctx is cancelled or a
// server fails fatally, then performs coordinated shutdown. Returns nil on
// clean shutdown (ctx cancelled), or the fatal error.
func (r *Runtime) Run(ctx context.Context) error {
	r.createServers(ctx)

	// errCh collects fatal errors from goroutines without ever calling
	// os.Exit from a goroutine (which would skip deferred cleanup).
	errCh := make(chan error, 6)
	var wg sync.WaitGroup

	if r.tlsConfig != nil {
		r.startACMEHTTP(&wg, errCh)
	}

	starters := []struct {
		name string
		run  func() error
	}{
		{"SMTP receiving :" + r.cfg.SMTPPort, r.smtpReceiving.Start},
		{"SMTP submission :" + r.cfg.SubmissionPort, r.smtpSubmission.Start},
		{"IMAP :" + r.cfg.IMAPPort, r.imapStartTLS.Start},
		{"IMAPS :" + r.cfg.IMAPSPort, r.imapTLS.Start},
	}
	for _, s := range starters {
		wg.Add(1)
		go r.runServer(s.name, s.run, &wg, errCh)
	}

	var fatalErr error
	select {
	case <-ctx.Done():
		log.Println("Shutting down...")
	case err := <-errCh:
		log.Printf("Fatal: %v", err)
		fatalErr = err
	}

	r.shutdown(ctx)

	// Give in-flight goroutines a chance to return, then bail. Protocol
	// sessions stuck mid-DB-call are cancelled by the context already, so
	// a brief wait is enough.
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

	return fatalErr
}

func (r *Runtime) createServers(ctx context.Context) {
	r.smtpReceiving = smtp.NewServer(ctx, r.store, r.authn, ":"+r.cfg.SMTPPort, nil, false)
	r.smtpSubmission = smtp.NewServer(ctx, r.store, r.authn, ":"+r.cfg.SubmissionPort, r.tlsConfig, true)
	r.imapStartTLS = imap.NewServer(ctx, r.store, r.authn, ":"+r.cfg.IMAPPort, r.tlsConfig, false)
	r.imapTLS = imap.NewServer(ctx, r.store, r.authn, ":"+r.cfg.IMAPSPort, r.tlsConfig, true)
}

func (r *Runtime) runServer(name string, fn func() error, wg *sync.WaitGroup, errCh chan<- error) {
	defer wg.Done()
	log.Printf("Starting %s...", name)
	if err := fn(); err != nil {
		if errors.Is(err, net.ErrClosed) {
			return
		}
		errCh <- fmt.Errorf("%s: %w", name, err)
	}
}

func (r *Runtime) startACMEHTTP(wg *sync.WaitGroup, errCh chan<- error) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("Starting ACME HTTP challenge server on :80")
		acmeSrv := &http.Server{
			Addr:     ":80",
			Handler:  r.acmeMgr.HTTPHandler(nil),
			ErrorLog: log.Default(),
		}
		r.acmeHTTP = acmeSrv
		if err := acmeSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errCh <- fmt.Errorf("ACME HTTP server: %w", err)
		}
	}()
}

func (r *Runtime) shutdown(ctx context.Context) {
	r.smtpReceiving.Stop()
	r.smtpSubmission.Stop()
	r.imapStartTLS.Stop()
	r.imapTLS.Stop()
	if r.acmeHTTP != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := r.acmeHTTP.Shutdown(shutdownCtx); err != nil {
			log.Printf("ACME HTTP shutdown: %v", err)
		}
		cancel()
	}
}

// Close releases resources (database pool). Safe to call after Run returns
// or if Init failed.
func (r *Runtime) Close() {
	if r.store != nil {
		r.store.Close()
	}
}
