package keyholder

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func lose(f *fakeRunner, p Pair) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[p.AgentUnit] = false
}

func TestFingerprintOfAnInvalidKeyIsRefused(t *testing.T) {
	for _, bad := range []string{"", "ssh-ed25519", "ssh-ed25519 !!!notbase64 c"} {
		if fp, err := Fingerprint(bad); err == nil {
			t.Errorf("Fingerprint(%q) = %q, want an error", bad, fp)
		}
	}
	fp, err := Fingerprint("ssh-ed25519 AAAAC3NzaFAKEAAA holder:reader")
	if err != nil || !strings.HasPrefix(fp, "SHA256:") || strings.Contains(fp, "=") {
		t.Errorf("Fingerprint = %q, %v, want an unpadded SHA256: value", fp, err)
	}
}

func TestAFirstMintAndALivePairAreNotReminted(t *testing.T) {
	l, _, _ := launcher(t)
	p1, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p1.Reminted != nil || p1.Fingerprint == "" {
		t.Fatalf("first mint: Reminted = %+v, Fingerprint = %q; want no re-mint and a fingerprint", p1.Reminted, p1.Fingerprint)
	}
	p2, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Reminted != nil || p2.Fingerprint != p1.Fingerprint {
		t.Errorf("a live pair found again: Reminted = %+v, fingerprint %q vs %q", p2.Reminted, p2.Fingerprint, p1.Fingerprint)
	}
}

func TestALostPairReplacedReportsTheOldAndTheNewFingerprint(t *testing.T) {
	l, f, logs := launcher(t)
	p1, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	lose(f, p1)
	p2, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Reminted == nil {
		t.Fatal("a replaced pair did not report a re-mint")
	}
	if p2.Reminted.OldFingerprint != p1.Fingerprint || p2.Reminted.NewFingerprint != p2.Fingerprint || p1.Fingerprint == p2.Fingerprint {
		t.Errorf("Reminted = %+v; old pair %q, new pair %q", p2.Reminted, p1.Fingerprint, p2.Fingerprint)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), p1.Fingerprint) {
		t.Errorf("the lifecycle log does not carry the old fingerprint: %v", *logs)
	}
	// And a second loss chains from the key that replaced the first.
	lose(f, p2)
	p3, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p3.Reminted == nil || p3.Reminted.OldFingerprint != p2.Fingerprint {
		t.Errorf("second re-mint = %+v, want old = %q", p3.Reminted, p2.Fingerprint)
	}
}

func TestAStoppedPairEndsTheKeyHistorySoTheNextMintIsNotAChange(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LastPath(l.Base, "s/a")); !os.IsNotExist(err) {
		t.Errorf("the key history survived a deliberate stop: %v", err)
	}
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Reminted != nil {
		t.Errorf("a session started again after a deliberate stop reports a re-mint: %+v", p.Reminted)
	}
}

func TestANoticeAboutAReMintIsRateLimitedAndCountsWhatItHeldBack(t *testing.T) {
	l, f, _ := launcher(t)
	p1, _ := l.Start(context.Background(), "s/a")
	lose(f, p1)
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if send, n := l.ClaimNotice("s/a", t0); !send || n != 0 {
		t.Fatalf("first claim = %v, %d; want to send, nothing held back", send, n)
	}
	for i := 1; i <= 3; i++ {
		if send, _ := l.ClaimNotice("s/a", t0.Add(time.Duration(i)*time.Minute)); send {
			t.Fatalf("claim %d inside the interval was allowed", i)
		}
	}
	send, n := l.ClaimNotice("s/a", t0.Add(NoticeInterval+time.Second))
	if !send || n != 3 {
		t.Errorf("claim after the interval = %v, %d; want to send, 3 held back", send, n)
	}
	if send, _ := l.ClaimNotice("s/none", t0); send {
		t.Error("a session with no key history was allowed a notice")
	}
}

func TestHealthIsOkDeadOrRemintedUntilACertificateIsInstalled(t *testing.T) {
	l, f, _ := launcher(t)
	ctx := context.Background()
	if h, err := l.HealthOf(ctx, "s/a"); err != nil || h.State != "none" || h.Note() != "" {
		t.Fatalf("no pair: %+v, %v; want none and no note", h, err)
	}
	p1, _ := l.Start(ctx, "s/a")
	if h, _ := l.HealthOf(ctx, "s/a"); h.State != "ok" || h.Note() != "" || h.Fingerprint != p1.Fingerprint {
		t.Fatalf("live pair: %+v; want ok, no note, its fingerprint", h)
	}
	lose(f, p1)
	if h, _ := l.HealthOf(ctx, "s/a"); h.State != "dead" || !strings.Contains(h.Note(), "DEAD") {
		t.Fatalf("lost pair: %+v; want dead with a note", h)
	}
	p2, _ := l.Start(ctx, "s/a")
	h, _ := l.HealthOf(ctx, "s/a")
	if h.State != "ok" || !h.Reminted || !strings.Contains(h.Note(), p1.Fingerprint) || !strings.Contains(h.Note(), p2.Fingerprint) {
		t.Fatalf("re-minted pair: %+v, note %q; want ok, re-minted, both fingerprints in the note", h, h.Note())
	}
	if err := os.WriteFile(CertPathIn(p2.Dir), []byte("ssh-ed25519-cert-v01@openssh.com AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.HealthOf(ctx, "s/a"); h.Reminted || h.Note() != "" {
		t.Errorf("with a certificate installed: %+v, note %q; want the re-mint note gone", h, h.Note())
	}
}
