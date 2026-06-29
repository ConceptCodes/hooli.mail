package runtime

import (
	"context"
	"testing"
)

// TestProvisionTLSWithoutDomain verifies that TLS is not provisioned when
// no domain is configured (dev mode). This pins the dev-mode path that
// every developer hits when running locally without a domain.
func TestProvisionTLSWithoutDomain(t *testing.T) {
	r := &Runtime{cfg: Config{}}
	r.provisionTLS()

	if r.tlsConfig != nil {
		t.Error("tlsConfig should be nil without a domain")
	}
	if r.acmeMgr != nil {
		t.Error("acmeMgr should be nil without a domain")
	}
}

// TestProvisionTLSWithDomain verifies that TLS and ACME manager are set up
// when a domain is configured.
func TestProvisionTLSWithDomain(t *testing.T) {
	r := &Runtime{cfg: Config{Domain: "mail.example.com", ACMEEmail: "admin@example.com"}}
	r.provisionTLS()

	if r.tlsConfig == nil {
		t.Fatal("tlsConfig should be set with a domain")
	}
	if r.acmeMgr == nil {
		t.Fatal("acmeMgr should be set with a domain")
	}
	if r.acmeMgr.Email != "admin@example.com" {
		t.Errorf("ACME email = %q, want admin@example.com", r.acmeMgr.Email)
	}

	err := r.acmeMgr.HostPolicy(context.Background(), "mail.example.com")
	if err != nil {
		t.Errorf("HostPolicy should accept the configured domain: %v", err)
	}
	err = r.acmeMgr.HostPolicy(context.Background(), "evil.com")
	if err == nil {
		t.Error("HostPolicy should reject an unconfigured domain")
	}
}

// TestCloseSafeWithoutInit verifies that Close does not panic when called
// on a Runtime that was never initialized. This pins the nil-guard that
// prevents a crash if Init fails partway through.
func TestCloseSafeWithoutInit(t *testing.T) {
	r := New(Config{})
	r.Close()
}

// TestNewStoresConfig verifies that New copies the Config into the Runtime
// so the caller's Config is not aliased after construction.
func TestNewStoresConfig(t *testing.T) {
	cfg := Config{
		DSN:      "postgres://test",
		Domain:   "mail.test",
		SMTPPort: "2525",
	}
	r := New(cfg)

	if r.cfg.DSN != cfg.DSN {
		t.Errorf("DSN = %q, want %q", r.cfg.DSN, cfg.DSN)
	}
	if r.cfg.Domain != cfg.Domain {
		t.Errorf("Domain = %q, want %q", r.cfg.Domain, cfg.Domain)
	}
	if r.cfg.SMTPPort != cfg.SMTPPort {
		t.Errorf("SMTPPort = %q, want %q", r.cfg.SMTPPort, cfg.SMTPPort)
	}
}
