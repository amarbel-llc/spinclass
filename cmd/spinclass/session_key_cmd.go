package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Seams for command tests.
var (
	sessionKeyFind    = session.FindByTarget
	sessionKeyOut     = func() io.Writer { return os.Stdout }
	sessionKeyBase    = keyholder.StateBase
	sessionKeyRunning = func(ctx context.Context, base, key string) (keyholder.Pair, bool, error) {
		l := &keyholder.Launcher{Base: base, Run: keyholder.SystemdRunner{}}
		return l.Running(ctx, key)
	}
	// sessionKeyAgentList runs `ssh-add -L` against sock and nothing else: the
	// environment is PATH, HOME and that one socket, never the operator's agent.
	sessionKeyAgentList = func(ctx context.Context, sock string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "ssh-add", "-L")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "SSH_AUTH_SOCK=" + sock}
		return cmd.Output()
	}
	// sessionKeyFingerprint prints the SHA256 fingerprint of one public key line.
	sessionKeyFingerprint = func(ctx context.Context, pub string) (string, error) {
		cmd := exec.CommandContext(ctx, "ssh-keygen", "-lf", "-")
		cmd.Stdin = strings.NewReader(pub + "\n")
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		f := strings.Fields(string(out))
		if len(f) < 2 || !strings.HasPrefix(f[1], "SHA256:") {
			return "", fmt.Errorf("unexpected ssh-keygen output %q", strings.TrimSpace(string(out)))
		}
		return f[1], nil
	}
	// sessionKeyCertExpiry reads the expiry of an installed certificate.
	sessionKeyCertExpiry = func(ctx context.Context, path string) (time.Time, error) {
		out, err := exec.CommandContext(ctx, "ssh-keygen", "-L", "-f", path).Output()
		if err != nil {
			return time.Time{}, err
		}
		return parseCertExpiry(string(out))
	}
)

// registerSessionKeyCommand registers `sc session-key SESSION`: print a
// session's key holder public key and fingerprint, so the operator can have it
// certified. Read-only and CLI-only.
func registerSessionKeyCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "session-key",
		Title: "Print a session's key holder public key",
		Description: command.Description{
			Short: "Print a session's key holder public key and fingerprint",
			Long: "Prints exactly two lines: the public key the session's holder lists (authorized_keys form) and its SHA256 fingerprint. " +
				"With --json: {session, public_key, fingerprint, cert_path, cert_expires}; cert_expires is null until a certificate is installed at cert_path. " +
				"Fails with exit 1 when the session has no holder or its holder is not running. Never mints and never starts anything.",
		},
		Params: []command.Param{
			{Name: "session", Type: command.String, Required: true, Description: "Session key (<repo>/<branch>) or worktree name"},
			{Name: "json", Type: command.Bool, Description: "Print one JSON object instead of the two lines"},
		},
		RunCLI: runSessionKey,
	})
}

func runSessionKey(ctx context.Context, args json.RawMessage) error {
	var f struct {
		Session string `json:"session"`
		JSON    bool   `json:"json"`
	}
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("session-key: %w", err)
	}
	if f.Session == "" {
		return errors.New("session-key: --session is required")
	}
	st, err := sessionKeyFind(f.Session)
	if err != nil {
		return fmt.Errorf("session-key: %w", err)
	}
	key := st.Key()
	base := sessionKeyBase()
	if _, serr := os.Lstat(keyholder.SessionDir(base, key)); serr != nil {
		return fmt.Errorf("session-key: no holder for session %s", key)
	}
	pair, running, err := sessionKeyRunning(ctx, base, key)
	if err != nil {
		return fmt.Errorf("session-key: %w", err)
	}
	if !running {
		return fmt.Errorf("session-key: holder not running for session %s", key)
	}
	listed, err := sessionKeyAgentList(ctx, pair.AgentSock)
	if err != nil {
		return fmt.Errorf("session-key: asking the holder for its key: %w", err)
	}
	pub, err := onlyKeyLine(listed)
	if err != nil {
		return fmt.Errorf("session-key: %w", err)
	}
	fp, err := sessionKeyFingerprint(ctx, pub)
	if err != nil {
		return fmt.Errorf("session-key: fingerprint: %w", err)
	}
	certPath := keyholder.CertPath(base, key)
	var expires *time.Time
	if _, serr := os.Stat(certPath); serr == nil {
		if t, cerr := sessionKeyCertExpiry(ctx, certPath); cerr == nil {
			expires = &t
		}
	}
	out := sessionKeyOut()
	if f.JSON {
		return json.NewEncoder(out).Encode(struct {
			Session     string     `json:"session"`
			PublicKey   string     `json:"public_key"`
			Fingerprint string     `json:"fingerprint"`
			CertPath    string     `json:"cert_path"`
			CertExpires *time.Time `json:"cert_expires"`
		}{key, pub, fp, certPath, expires})
	}
	fmt.Fprintf(out, "%s\n%s\n", pub, fp)
	return nil
}

// onlyKeyLine returns the single key line `ssh-add -L` printed. A holder lists
// exactly one key; any other count means the socket is not this session's
// holder, and nothing is guessed.
func onlyKeyLine(listed []byte) (string, error) {
	var keys []string
	for _, line := range strings.Split(string(bytes.TrimSpace(listed)), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			keys = append(keys, l)
		}
	}
	if len(keys) != 1 {
		return "", fmt.Errorf("the holder lists %d keys, want exactly one", len(keys))
	}
	return keys[0], nil
}

// parseCertExpiry reads the end of the validity from `ssh-keygen -L` output
// ("Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00"). ssh-keygen prints
// local time without a zone, so the instant is read in the local zone.
func parseCertExpiry(out string) (time.Time, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Valid:") {
			continue
		}
		i := strings.LastIndex(line, " to ")
		if i < 0 {
			break
		}
		return time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(line[i+4:]), time.Local)
	}
	return time.Time{}, errors.New("no Valid line in the certificate")
}
