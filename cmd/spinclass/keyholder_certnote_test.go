package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// sc list and session-health say what the installed certificate is: none (only in
// session-health, which says a healthy pair too), valid until a time, for another
// key, expired, or unusable.
func TestKeyholderNoteSaysNoCertificateValidAnotherKeyExpiredOrUnusable(t *testing.T) {
	khRemintEnv(t)
	stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})

	inner := keyholderStatusLauncherFn
	listing := ""
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.Local)
	keyholderStatusLauncherFn = func() *keyholder.Launcher {
		l := inner()
		l.CertList = func(context.Context, []byte) (string, error) { return listing, nil }
		l.Now = func() time.Time { return now }
		return l
	}
	t.Cleanup(func() { keyholderStatusLauncherFn = inner })
	h, err := keyholderStatusLauncherFn().HealthOf(context.Background(), "worker/kid")
	if err != nil || h.Fingerprint == "" {
		t.Fatalf("health: %+v, %v", h, err)
	}
	listingFor := func(fp string) string {
		return "        Public key: ED25519-CERT " + fp + "\n        Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00\n"
	}

	if n := keyholderNote("worker/kid", false); n != "" {
		t.Errorf("sc list with no certificate says %q, want nothing", n)
	}
	if n := keyholderNote("worker/kid", true); !strings.Contains(n, "no certificate") {
		t.Errorf("session-health with no certificate says %q, want 'no certificate'", n)
	}

	cert := keyholder.CertPath(keyholderStateBaseFn(), "worker/kid")
	if err := os.WriteFile(cert, []byte("ssh-ed25519-cert-v01@openssh.com AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing = listingFor(h.Fingerprint)
	for _, includeOK := range []bool{false, true} {
		if n := keyholderNote("worker/kid", includeOK); !strings.Contains(n, "certificate valid until ") {
			t.Errorf("includeOK=%v with a valid certificate: %q, want 'certificate valid until'", includeOK, n)
		}
	}
	listing = listingFor("SHA256:SOMEONEELSE")
	if n := keyholderNote("worker/kid", false); !strings.Contains(n, "ANOTHER key") {
		t.Errorf("a certificate for another key: %q", n)
	}
	listing = listingFor(h.Fingerprint)
	now = time.Date(2026, 11, 28, 0, 0, 0, 0, time.Local)
	if n := keyholderNote("worker/kid", false); !strings.Contains(n, "EXPIRED") {
		t.Errorf("an expired certificate: %q", n)
	}
	if err := os.WriteFile(cert, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := keyholderNote("worker/kid", false); !strings.Contains(n, "unusable") {
		t.Errorf("an unusable certificate: %q", n)
	}
}
