package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// The absolute store paths of ssh-add, ssh-keygen, systemd-run and systemctl,
// burned in by lib.mkSpinclass (-X main.sshAddBin=...). Unlike the other tool
// pins, an empty value here is NOT "look the name up on PATH": the key holder
// runs in whatever process starts or inspects a session, and a name resolved
// from a PATH a session can influence is the hole (the same rule the fibby and
// piggy settings state). An unpinned build (go test, a dev build) refuses,
// naming the pin; the tests set these variables themselves.
var (
	sshAddBin     = ""
	sshKeygenBin  = ""
	systemdRunBin = ""
	systemctlBin  = ""
)

func init() { keyholder.SetSystemdPins(systemdRunBin, systemctlBin) }

// errSessionKeyToolUnpinned is what a build without a tool's pin yields.
var errSessionKeyToolUnpinned = errors.New("this build has no pinned path for the tool and the key holder never looks a tool up on PATH")

func pinnedSessionKeyTool(name, pin string) (string, error) {
	if pin == "" {
		return "", fmt.Errorf("session-key: %s: %w (pins are burned in by lib.mkSpinclass: -X main.%s)", name, errSessionKeyToolUnpinned, map[string]string{"ssh-add": "sshAddBin", "ssh-keygen": "sshKeygenBin"}[name])
	}
	return pin, nil
}

func sessionKeySSHAdd() (string, error) { return pinnedSessionKeyTool("ssh-add", sshAddBin) }

func sessionKeySSHKeygen() (string, error) {
	return pinnedSessionKeyTool("ssh-keygen", sshKeygenBin)
}

// Seams for command tests.
var (
	sessionKeyFind    = session.FindByTarget
	sessionKeyOut     = func() io.Writer { return os.Stdout }
	sessionKeyErr     = func() io.Writer { return os.Stderr }
	sessionKeyBase    = keyholder.StateBase
	sessionKeyNow     = time.Now
	sessionKeyRunning = func(ctx context.Context, base, key string) (keyholder.Pair, bool, error) {
		l := &keyholder.Launcher{Base: base, Run: keyholder.SystemdRunner{}}
		return l.Running(ctx, key)
	}
	// sessionKeyAgentList runs `ssh-add -L` against sock and nothing else: the
	// environment is PATH, HOME and that one socket, never the operator's agent.
	sessionKeyAgentList = func(ctx context.Context, sock string) ([]byte, error) {
		bin, err := sessionKeySSHAdd()
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, bin, "-L")
		cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "SSH_AUTH_SOCK=" + sock}
		return cmd.Output()
	}
	// sessionKeyFingerprint prints the SHA256 fingerprint of one public key line.
	sessionKeyFingerprint = func(ctx context.Context, pub string) (string, error) {
		bin, err := sessionKeySSHKeygen()
		if err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, bin, "-lf", "-")
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
	// sessionKeyCertList lists one certificate (`ssh-keygen -L`). The certificate
	// is handed over as bytes on stdin, so a file swapped after it was checked is
	// never what ssh-keygen reads.
	sessionKeyCertList keyholder.CertLister = func(ctx context.Context, cert []byte) (string, error) {
		bin, err := sessionKeySSHKeygen()
		if err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, bin, "-L", "-f", "/dev/stdin")
		cmd.Stdin = bytes.NewReader(cert)
		out, err := cmd.Output()
		return string(out), err
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
			Long: "Prints exactly two lines on stdout: the public key line exactly as the session's holder lists it (authorized_keys form) and its SHA256 fingerprint. " +
				"With --json: {session, public_key, fingerprint, cert_path, cert_expires, cert_matches_key, cert_problem}; session is always the canonical session key (<repo>/<branch>), whatever form was passed. cert_path is where the certificate is installed (write then rename, into its directory only); " +
				"cert_expires (UTC) and cert_matches_key are read from the installed file with ssh-keygen and are null until one is installed; cert_problem says why an installed file is unusable. " +
				"A certificate for another key, an expired one or an unusable one is a warning on stderr; stdout stays the two lines. " +
				"Fails with exit 1 when the session has no holder, its holder is not running, the socket lists more than one key, the listed line is not one clean key line (over 4 KiB, a control or format character, a type other than ssh-ed25519 and ecdsa-sha2-nistp256, a blob that is not base64), or the listed key is not the one spinclass recorded when it minted the pair (\"the holder's key changed since the pair started\"). " +
				"For an unconfined session the printed key is still the session's own claim about its socket (same uid, no boundary); the check against the recorded fingerprint is what stops a replaced or re-minted key from being certified unnoticed. Never mints and never starts anything.",
		},
		Params: []command.Param{
			{Name: "session", Type: command.String, Required: true, Description: "Session key (<repo>/<branch>) or worktree name"},
			{Name: "json", Type: command.Bool, Description: "Print one JSON object instead of the two lines"},
		},
		Examples: []command.Example{
			{Description: "Print a session's key holder public key and fingerprint (two lines), for the operator to certify", Command: "sc session-key repo/branch"},
			{Description: "The same as one JSON object, with the certificate path and what is known about an installed certificate", Command: "sc session-key --json repo/branch"},
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
	// The listed line is what the socket says; it is checked before anything
	// shells out with it, and before it can reach the operator's terminal.
	if err := validateKeyLine(pub); err != nil {
		return fmt.Errorf("session-key: %w", err)
	}
	fp, err := sessionKeyFingerprint(ctx, pub)
	if err != nil {
		return fmt.Errorf("session-key: fingerprint: %w", err)
	}
	// The key spinclass minted when it started this pair is the truth. A socket
	// that lists another key (a session that retired and minted again over its own
	// socket, or put its own agent where the holder's was) is not vouched for.
	if pair.Fingerprint == "" {
		return fmt.Errorf("session-key: the pair's record carries no fingerprint recorded at the mint, so nothing is vouched for; stop the pair and start it again")
	}
	if fp != pair.Fingerprint {
		return fmt.Errorf("session-key: the holder's key changed since the pair started (listed %s, minted %s): refusing to print it; stop the pair and start it again", fp, pair.Fingerprint)
	}
	certPath := keyholder.CertPath(base, key)
	cert := keyholder.InspectCert(ctx, certPath, fp, sessionKeyCertList, sessionKeyNow())
	if w := certWarning(cert, fp); w != "" {
		fmt.Fprintln(sessionKeyErr(), "session-key: warning: "+w)
	}
	out := sessionKeyOut()
	if f.JSON {
		var expires *time.Time
		var matches *bool
		var problem *string
		if cert.State != keyholder.CertNone && cert.State != keyholder.CertUnreadable {
			m := cert.State != keyholder.CertOtherKey
			matches, expires = &m, cert.Expires
		}
		if cert.State == keyholder.CertUnreadable {
			problem = &cert.Problem
		}
		return json.NewEncoder(out).Encode(struct {
			Session        string     `json:"session"`
			PublicKey      string     `json:"public_key"`
			Fingerprint    string     `json:"fingerprint"`
			CertPath       string     `json:"cert_path"`
			CertExpires    *time.Time `json:"cert_expires"`
			CertMatchesKey *bool      `json:"cert_matches_key"`
			CertProblem    *string    `json:"cert_problem"`
		}{key, pub, fp, certPath, expires, matches, problem})
	}
	fmt.Fprintf(out, "%s\n%s\n", pub, fp)
	return nil
}

// certWarning is the stderr line for an installed certificate that is not a
// current certificate for the session's key, or "".
func certWarning(c keyholder.CertInfo, fp string) string {
	switch c.State {
	case keyholder.CertOtherKey:
		return fmt.Sprintf("the installed certificate certifies another key (%s), not this session's (%s)", c.Fingerprint, fp)
	case keyholder.CertExpired:
		return "the installed certificate expired at " + c.Expires.UTC().Format(time.RFC3339)
	case keyholder.CertUnreadable:
		return "the installed certificate is unusable: " + c.Problem
	}
	return ""
}

// maxKeyLineBytes bounds the line the holder may list: a real key line is a few
// hundred bytes, and the line is printed and, in JSON, copied into a document.
const maxKeyLineBytes = 4096

// validateKeyLine refuses a listed line that is not one clean public key line of
// a type spinclass accepts: too long, any control or format character (escape
// sequences, carriage returns, tabs, bidirectional overrides reach the
// operator's terminal or whatever he pastes it into), a type outside ed25519 and
// nistp256, or a blob that is not base64.
func validateKeyLine(pub string) error {
	if len(pub) > maxKeyLineBytes {
		return fmt.Errorf("the key line is longer than %d bytes", maxKeyLineBytes)
	}
	for _, r := range pub {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("the key line contains a control character (%U)", r)
		}
	}
	f := strings.Fields(pub)
	if len(f) < 2 {
		return errors.New("not a public key line (want: type, base64 blob, comment)")
	}
	switch f[0] {
	case "ssh-ed25519", "ecdsa-sha2-nistp256":
	default:
		return fmt.Errorf("the key type %q is not ssh-ed25519 or ecdsa-sha2-nistp256", f[0])
	}
	if _, err := base64.StdEncoding.DecodeString(f[1]); err != nil {
		return fmt.Errorf("the key blob is not base64: %w", err)
	}
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
