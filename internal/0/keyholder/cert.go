package keyholder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CertEnv is the environment variable that names the installed certificate: a
// session finds its certificate at that path (read-only inside a confined unit,
// where it is the one file of the bound certificate directory).
const CertEnv = "SPINCLASS_KEYHOLDER_CERT"

// MaxCertBytes bounds the certificate file a reader will take. A real
// certificate is under a kilobyte; the cap only keeps a hostile file small.
const MaxCertBytes = 16 << 10

// CertDirIn is the directory a certificate is installed into, inside a session
// directory: created 0700 by the pair launch, written into by whoever signs
// the certificate (write then rename, into this directory only) and bound
// read-only into a confined unit. A directory, not the file, because a bind of
// one file keeps the old inode after a rename over it.
func CertDirIn(dir string) string { return filepath.Join(dir, "cert") }

// CertPathIn is the certificate file inside a session directory: the one place
// the install path is computed.
func CertPathIn(dir string) string { return filepath.Join(CertDirIn(dir), "cert.pub") }

// CertDirOfAgentSock is the certificate directory of the pair whose agent
// socket is sock (<session dir>/agent/agent.sock).
func CertDirOfAgentSock(sock string) string {
	return CertDirIn(filepath.Dir(filepath.Dir(sock)))
}

// CertState is what is known about the installed certificate.
type CertState string

const (
	// CertNone: nothing is installed.
	CertNone CertState = "none"
	// CertValid: a certificate for this session's key that has not expired.
	CertValid CertState = "valid"
	// CertOtherKey: a certificate that certifies a key other than the holder's.
	CertOtherKey CertState = "other-key"
	// CertExpired: a certificate for this key whose validity has ended.
	CertExpired CertState = "expired"
	// CertUnreadable: a file is there but is not a usable certificate (a link,
	// too big, several lines, not a certificate, or ssh-keygen would not read it).
	CertUnreadable CertState = "unreadable"
)

// CertInfo is the result of inspecting an installed certificate.
type CertInfo struct {
	State CertState
	// Fingerprint is the SHA256 fingerprint of the key the certificate certifies.
	Fingerprint string
	// Expires is the end of the validity in UTC; nil for "forever" or none.
	Expires *time.Time
	// Problem says why the state is unreadable.
	Problem string
}

// CertLister runs `ssh-keygen -L` on a certificate given as bytes and returns
// its output. The bytes, not a path, are what is read, so a file swapped after
// the check cannot be what ssh-keygen sees.
type CertLister func(ctx context.Context, cert []byte) (string, error)

// ReadCertFile reads the installed certificate at path without following a
// link: (nil, false, nil) when nothing is there, an error when something is
// there that is not one regular, small, single-line certificate file.
func ReadCertFile(path string) ([]byte, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, false, nil
	case errors.Is(err, syscall.ELOOP):
		return nil, true, errors.New("the certificate path is a symbolic link: not followed")
	case err != nil:
		return nil, true, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, true, err
	}
	if !fi.Mode().IsRegular() {
		return nil, true, fmt.Errorf("the certificate path is not a regular file (mode %s)", fi.Mode().Type())
	}
	if fi.Size() > MaxCertBytes {
		return nil, true, fmt.Errorf("the certificate file is %d bytes, over the %d-byte limit", fi.Size(), MaxCertBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxCertBytes+1))
	if err != nil {
		return nil, true, err
	}
	if len(b) > MaxCertBytes {
		return nil, true, fmt.Errorf("the certificate file is over the %d-byte limit", MaxCertBytes)
	}
	line := string(bytes.TrimSuffix(b, []byte("\n")))
	if line == "" || strings.ContainsAny(line, "\n\r\x00") {
		return nil, true, errors.New("the certificate file is not one line")
	}
	f0 := strings.Fields(line)
	if len(f0) < 2 || !strings.HasSuffix(f0[0], "-cert-v01@openssh.com") {
		return nil, true, errors.New("the file is not an OpenSSH certificate (its type is not *-cert-v01@openssh.com)")
	}
	return []byte(line + "\n"), true, nil
}

// InspectCert reads the certificate at path and says whether it is a current
// certificate for the key whose fingerprint is wantFP. A symbolic link at path
// is reported, never followed.
func InspectCert(ctx context.Context, path, wantFP string, list CertLister, now time.Time) CertInfo {
	b, present, err := ReadCertFile(path)
	if !present {
		return CertInfo{State: CertNone}
	}
	if err != nil {
		return CertInfo{State: CertUnreadable, Problem: err.Error()}
	}
	out, err := list(ctx, b)
	if err != nil {
		return CertInfo{State: CertUnreadable, Problem: "ssh-keygen would not read it: " + err.Error()}
	}
	fp, exp, err := ParseCertListing(out)
	if err != nil {
		return CertInfo{State: CertUnreadable, Problem: err.Error()}
	}
	info := CertInfo{Fingerprint: fp, Expires: exp}
	switch {
	case fp != wantFP:
		info.State = CertOtherKey
	case exp != nil && !exp.After(now):
		info.State = CertExpired
	default:
		info.State = CertValid
	}
	return info
}

// ParseCertListing reads `ssh-keygen -L` output: the fingerprint of the key the
// certificate certifies ("Public key: ED25519-CERT SHA256:...", the same
// fingerprint as the plain key) and the end of its validity in UTC (nil for
// "Valid: forever").
func ParseCertListing(out string) (fingerprint string, expires *time.Time, err error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Public key:") && fingerprint == "" {
			f := strings.Fields(line)
			if last := f[len(f)-1]; strings.HasPrefix(last, "SHA256:") {
				fingerprint = last
			}
		}
	}
	if fingerprint == "" {
		return "", nil, errors.New("no key fingerprint in the certificate listing")
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Valid:") && strings.Contains(line, "forever") {
			return fingerprint, nil, nil
		}
	}
	t, err := ParseCertExpiry(out)
	if err != nil {
		return "", nil, err
	}
	return fingerprint, &t, nil
}

// ParseCertExpiry reads the end of the validity from `ssh-keygen -L` output
// ("Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00"). ssh-keygen prints
// local time without a zone, so the instant is read in the local zone and
// returned in UTC: the value is then the same whatever zone the host is in.
func ParseCertExpiry(out string) (time.Time, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Valid:") {
			continue
		}
		i := strings.LastIndex(line, " to ")
		if i < 0 {
			break
		}
		t, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(line[i+4:]), time.Local)
		if err != nil {
			return time.Time{}, err
		}
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("no Valid line in the certificate")
}
