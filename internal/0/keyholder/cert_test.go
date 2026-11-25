package keyholder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// listing is what a real `ssh-keygen -L -f key-cert.pub` printed for a throwaway
// certificate (just explore-cert-format); the certified key's fingerprint is the
// plain key's.
const listing = `key-cert.pub:
        Type: ssh-ed25519-cert-v01@openssh.com user certificate
        Public key: ED25519-CERT SHA256:cxObu09mgPzAX4ZsPUOKxgAJYMRGShR4SfJp+eJPB5A
        Signing CA: ED25519 SHA256:AV2HOrFZ3lXiOmAWzvuKHbxazQ/fiFGt6CSPRoPbLmw (using ssh-ed25519)
        Key ID: "id-1"
        Serial: 0
        Valid: from 2026-11-25T22:15:00 to 2026-11-25T23:16:09
        Principals: 
                flic:restic-snapshots
`

const listingFP = "SHA256:cxObu09mgPzAX4ZsPUOKxgAJYMRGShR4SfJp+eJPB5A"

const certLine = "ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQ comment"

func fakeList(out string, err error) CertLister {
	return func(context.Context, []byte) (string, error) { return out, err }
}

func writeCert(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(CertDirIn(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	p := CertPathIn(dir)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheCertificatePathIsOneFileInADirectoryNamedCertInsideTheSessionDirectory(t *testing.T) {
	if got, want := CertPathIn("/s/abc"), "/s/abc/cert/cert.pub"; got != want {
		t.Errorf("CertPathIn = %q, want %q", got, want)
	}
	if got, want := CertDirOfAgentSock("/s/abc/agent/agent.sock"), "/s/abc/cert"; got != want {
		t.Errorf("CertDirOfAgentSock = %q, want %q", got, want)
	}
	if got := CertPath("/base", "s/a"); got != filepath.Join("/base", Hash("s/a"), "cert", "cert.pub") {
		t.Errorf("CertPath = %q", got)
	}
}

func TestParseCertListingReadsTheCertifiedKeyAndTheEndOfTheValidityInUTC(t *testing.T) {
	prev := time.Local
	t.Cleanup(func() { time.Local = prev })
	time.Local = time.FixedZone("UTC+2", 2*3600)
	fp, exp, err := ParseCertListing(listing)
	if err != nil {
		t.Fatal(err)
	}
	if fp != listingFP {
		t.Errorf("fingerprint %q, want %q", fp, listingFP)
	}
	want := time.Date(2026, 11, 25, 21, 16, 9, 0, time.UTC) // 23:16:09 at UTC+2
	if exp == nil || !exp.Equal(want) || exp.Location() != time.UTC {
		t.Errorf("expiry %v, want %v in UTC", exp, want)
	}
	if _, exp, err := ParseCertListing(strings.Replace(listing, "from 2026-11-25T22:15:00 to 2026-11-25T23:16:09", "forever", 1)); err != nil || exp != nil {
		t.Errorf("a certificate valid forever: expiry %v, err %v; want none", exp, err)
	}
	if _, _, err := ParseCertListing("Valid: from x to y\n"); err == nil {
		t.Error("a listing with no key fingerprint was accepted")
	}
}

func TestReadCertFileRefusesALinkAnOversizeFileSeveralLinesAndAnythingNotACertificate(t *testing.T) {
	dir := t.TempDir()
	p := writeCert(t, dir, certLine+"\n")
	if b, present, err := ReadCertFile(p); err != nil || !present || string(b) != certLine+"\n" {
		t.Fatalf("a good certificate: %q, %v, %v", b, present, err)
	}
	if _, present, err := ReadCertFile(filepath.Join(dir, "nowhere")); present || err != nil {
		t.Errorf("absent file: present %v, err %v; want neither", present, err)
	}
	// a symbolic link is reported, never followed
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte(certLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(CertDirIn(dir), "linked.pub")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, present, err := ReadCertFile(link); !present || err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a symlinked certificate: present %v, err %v; want 'symbolic link'", present, err)
	}
	for name, content := range map[string]string{
		"oversize":      "ssh-ed25519-cert-v01@openssh.com " + strings.Repeat("A", MaxCertBytes),
		"two lines":     certLine + "\n" + certLine + "\n",
		"not a cert":    "ssh-ed25519 AAAAC3Nza comment\n",
		"empty":         "\n",
		"a carriage rt": certLine + "\r\n",
	} {
		q := writeCert(t, t.TempDir(), content)
		if _, present, err := ReadCertFile(q); !present || err == nil {
			t.Errorf("%s: present %v, err %v; want an error", name, present, err)
		}
	}
	if _, present, err := ReadCertFile(CertDirIn(dir)); !present || err == nil {
		t.Errorf("a directory in place of the file: present %v, err %v; want an error", present, err)
	}
}

func TestInspectCertSaysValidOtherKeyExpiredUnreadableOrNone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := CertPathIn(dir)
	before := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	after := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	if got := InspectCert(ctx, path, listingFP, fakeList(listing, nil), before); got.State != CertNone {
		t.Errorf("nothing installed: %+v", got)
	}
	writeCert(t, dir, certLine+"\n")
	cases := []struct {
		name string
		got  CertInfo
		in   CertState
	}{
		{name: "valid", in: CertValid, got: InspectCert(ctx, path, listingFP, fakeList(listing, nil), before)},
		{name: "another key", in: CertOtherKey, got: InspectCert(ctx, path, "SHA256:someoneelse", fakeList(listing, nil), before)},
		{name: "expired", in: CertExpired, got: InspectCert(ctx, path, listingFP, fakeList(listing, nil), after)},
		{name: "ssh-keygen fails", in: CertUnreadable, got: InspectCert(ctx, path, listingFP, fakeList("", errors.New("exit 255")), before)},
		{name: "unparsable listing", in: CertUnreadable, got: InspectCert(ctx, path, listingFP, fakeList("junk\n", nil), before)},
	}
	for _, c := range cases {
		if c.got.State != c.in {
			t.Errorf("%s: state %q (%s), want %q", c.name, c.got.State, c.got.Problem, c.in)
		}
	}
	if got := cases[1].got; got.Fingerprint != listingFP {
		t.Errorf("another key: certified fingerprint %q, want %q", got.Fingerprint, listingFP)
	}
}

func TestTheLaunchCreatesTheCertificateDirectoryExactly0700WhateverTheUmask(t *testing.T) {
	for _, umask := range []int{0o000, 0o022, 0o077} {
		old := syscall.Umask(umask)
		l, _, _ := launcher(t)
		p, err := l.Start(context.Background(), "s/a")
		syscall.Umask(old)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{l.Base, p.Dir, CertDirIn(p.Dir)} {
			fi, err := os.Lstat(d)
			if err != nil {
				t.Fatal(err)
			}
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				t.Errorf("umask %04o: %s is not a plain directory (%s)", umask, d, fi.Mode())
			}
			if fi.Mode().Perm() != 0o700 {
				t.Errorf("umask %04o: %s has mode %o, want exactly 0700", umask, d, fi.Mode().Perm())
			}
			if !ownedByUs(fi) {
				t.Errorf("%s is not owned by this user", d)
			}
		}
		cert := CertDirIn(p.Dir)
		if real, err := filepath.EvalSymlinks(cert); err != nil || real != cert {
			t.Errorf("umask %04o: the certificate directory %s is not its own canonical path (%s, %v)", umask, cert, real, err)
		}
	}
}

func TestAPairLaunchedBeforeTheCertificateDirectoryExistedGetsOne(t *testing.T) {
	l, _, _ := launcher(t)
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(CertDirIn(p.Dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(CertDirIn(p.Dir)); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("the live pair's certificate directory after a second Start: %v, %v", fi, err)
	}
}

func TestACertificateDirectoryOfAnotherModeIsSetBackToExactly0700(t *testing.T) {
	l, _, _ := launcher(t)
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{CertDirIn(p.Dir), p.Dir, l.Base} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{CertDirIn(p.Dir), p.Dir, l.Base} {
		if fi, err := os.Lstat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s after a Start of a live pair: %v, %v; want exactly 0700", d, fi, err)
		}
	}
}

func TestACertificateDirectoryThatIsALinkIsRefused(t *testing.T) {
	l, _, _ := launcher(t)
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(CertDirIn(p.Dir)); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, CertDirIn(p.Dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background(), "s/a"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a linked certificate directory: err %v, want a refusal", err)
	}
}

func TestHealthTellsAValidAnotherKeyAnExpiredAndAnUnusableCertificate(t *testing.T) {
	l, _, _ := launcher(t)
	ctx := context.Background()
	p, err := l.Start(ctx, "s/a")
	if err != nil {
		t.Fatal(err)
	}
	l.Now = func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local) }
	l.CertList = fakeList(listing, nil)
	// the holder's recorded fingerprint is the pair's own: make the listing agree
	// or disagree with it
	agree := strings.Replace(listing, listingFP, p.Fingerprint, 1)
	if h, _ := l.HealthOf(ctx, "s/a"); h.Cert.State != CertNone || h.Note() != "" {
		t.Errorf("no certificate: %+v, note %q; want none and no note", h.Cert, h.Note())
	}
	writeCert(t, p.Dir, certLine+"\n")
	l.CertList = fakeList(agree, nil)
	h, _ := l.HealthOf(ctx, "s/a")
	if h.Cert.State != CertValid || !strings.Contains(h.Note(), "certificate valid until ") {
		t.Errorf("valid: %+v, note %q", h.Cert, h.Note())
	}
	l.CertList = fakeList(listing, nil) // certifies a key that is not this pair's
	h, _ = l.HealthOf(ctx, "s/a")
	if h.Cert.State != CertOtherKey || !strings.Contains(h.Note(), "ANOTHER key") {
		t.Errorf("another key: %+v, note %q", h.Cert, h.Note())
	}
	l.CertList = fakeList(agree, nil)
	l.Now = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.Local) }
	if h, _ = l.HealthOf(ctx, "s/a"); h.Cert.State != CertExpired || !strings.Contains(h.Note(), "EXPIRED") {
		t.Errorf("expired: %+v, note %q", h.Cert, h.Note())
	}
	l.CertList = fakeList("", errors.New("exit 255"))
	if h, _ = l.HealthOf(ctx, "s/a"); h.Cert.State != CertUnreadable || !strings.Contains(h.Note(), "unusable") {
		t.Errorf("unreadable: %+v, note %q", h.Cert, h.Note())
	}
}

func TestARemintedKeyStaysReportedUntilACertificateForTheNewKeyIsInstalled(t *testing.T) {
	l, f, _ := launcher(t)
	ctx := context.Background()
	p1, _ := l.Start(ctx, "s/a")
	lose(f, p1)
	p2, err := l.Start(ctx, "s/a")
	if err != nil {
		t.Fatal(err)
	}
	l.Now = func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local) }
	writeCert(t, p2.Dir, certLine+"\n")
	// the OLD key's certificate does not end the re-mint report
	l.CertList = fakeList(strings.Replace(listing, listingFP, p1.Fingerprint, 1), nil)
	if h, _ := l.HealthOf(ctx, "s/a"); !h.Reminted || h.Cert.State != CertOtherKey {
		t.Errorf("a certificate for the old key: reminted %v, cert %+v; want still re-minted, another key", h.Reminted, h.Cert)
	}
	l.CertList = fakeList(strings.Replace(listing, listingFP, p2.Fingerprint, 1), nil)
	if h, _ := l.HealthOf(ctx, "s/a"); h.Reminted || h.Cert.State != CertValid {
		t.Errorf("a certificate for the new key: reminted %v, cert %+v; want the re-mint report gone", h.Reminted, h.Cert)
	}
}
