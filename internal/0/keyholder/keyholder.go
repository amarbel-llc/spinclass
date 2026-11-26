// Package keyholder launches and tears down a session's private key holder: a
// fibby (a virtual smart card) and a piggy agent in holder mode, each a
// transient user unit, under a 0700 per-session directory. Minting a key needs
// no real card; the key lives in fibby's memory, so "the same key" holds for the
// pair's life only: a lost pair is detected on the next Start and re-minted, and
// the new key is logged (docs/plans/2026-11-25-key-holder.md).
//
// systemd and process execution sit behind Runner, so the logic is tested with a
// fake; the real runner is SystemdRunner.
package keyholder

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Scope is the holder scope name the pair is started and minted under. The
// scope kind is "any": the key is usable from the session's own unit, which is
// a different caller from the frontend that minted it.
const Scope = "spinclass-v1"

// MaxSocketPath is the longest unix socket path the kernel accepts (108 with
// the terminating NUL); a longer one is refused before anything is started.
const MaxSocketPath = 107

// Tools names the two binaries the pair needs, as absolute paths.
type Tools struct {
	Fibby string
	Piggy string
}

// Runner is everything the launcher asks of the system.
type Runner interface {
	// Start runs argv as a transient user unit with env set, and returns once
	// systemd has accepted it (not once the program is ready).
	Start(ctx context.Context, unit string, argv, env []string) error
	// Active reports whether the unit is running.
	Active(ctx context.Context, unit string) (bool, error)
	// Stop stops the unit; stopping a unit that is not loaded is not an error.
	Stop(ctx context.Context, unit string) error
	// Output runs argv to completion with env and returns its stdout.
	Output(ctx context.Context, argv, env []string) ([]byte, error)
}

// Launcher manages the pairs under Base.
type Launcher struct {
	// Base is <state>/spinclass/keyholder; each session has a subdirectory.
	Base  string
	Tools Tools
	Run   Runner
	// Log receives one line per notable event; nil discards.
	Log func(format string, args ...any)
	// Wait bounds each wait for a socket or a unit to change state; zero means
	// five seconds.
	Wait time.Duration
	// CertList, when set, lets HealthOf inspect the installed certificate (it
	// runs `ssh-keygen -L`); nil means only its presence is known. Now is the
	// clock for expiry; nil means time.Now.
	CertList CertLister
	Now      func() time.Time
}

// Pair is a started pair.
type Pair struct {
	Session   string `json:"session"`
	FibbyUnit string `json:"fibby_unit"`
	AgentUnit string `json:"agent_unit"`
	PublicKey string `json:"public_key"`
	// Fingerprint is the key's SHA256 fingerprint, recorded at the mint.
	Fingerprint string    `json:"fingerprint,omitempty"`
	MintedAt    time.Time `json:"minted_at"`
	// Reminted is non-nil only on the Pair a Start call returned after it
	// replaced a LOST pair: the session has a different key now.
	Reminted *Remint `json:"-"`
	// Dir is the session's directory; AgentSock is the holder's agent socket,
	// the only socket a session is ever pointed at.
	Dir       string `json:"-"`
	AgentSock string `json:"-"`
	// Created is true only on the Pair a Start call launched afresh (a live pair
	// it found and left alone, and one read back by Running, carry false).
	Created bool `json:"-"`
}

// Hash is the short, stable name a session's files and units carry: the first
// eight bytes of sha256(session key), in hex.
func Hash(sessionKey string) string {
	h := sha256.Sum256([]byte(sessionKey))
	return fmt.Sprintf("%x", h[:8])
}

// StateBase returns <state>/spinclass/keyholder for the user's state directory
// ($XDG_STATE_HOME or ~/.local/state).
func StateBase() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "spinclass", "keyholder")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "spinclass", "keyholder")
}

// SessionDir returns a session's directory under base.
func SessionDir(base, sessionKey string) string { return filepath.Join(base, Hash(sessionKey)) }

// CertPath is where a certificate handed to the session is installed.
func CertPath(base, sessionKey string) string {
	return CertPathIn(SessionDir(base, sessionKey))
}

func (l *Launcher) logf(format string, args ...any) {
	if l.Log != nil {
		l.Log(format, args...)
	}
}

func (l *Launcher) wait() time.Duration {
	if l.Wait > 0 {
		return l.Wait
	}
	return 5 * time.Second
}

type layout struct {
	dir, fibbyDir, agentDir, workDir string
	certDir                          string
	ctl, pcsc, agent, record         string
	fibbyUnit, agentUnit             string
}

func (l *Launcher) layoutFor(sessionKey string) layout { return l.layoutForHash(Hash(sessionKey)) }

// layoutForHash is layoutFor for a pair found by its hash alone (a sweep finds
// directories and units whose session key is not known).
func (l *Launcher) layoutForHash(h string) layout {
	dir := filepath.Join(l.Base, h)
	return layout{
		dir:       dir,
		fibbyDir:  filepath.Join(dir, "fibby"),
		agentDir:  filepath.Join(dir, "agent"),
		workDir:   filepath.Join(dir, "work"),
		certDir:   CertDirIn(dir),
		ctl:       filepath.Join(dir, "fibby", "ctl.sock"),
		pcsc:      filepath.Join(dir, "fibby", "pcscd.comm"),
		agent:     filepath.Join(dir, "agent", "agent.sock"),
		record:    filepath.Join(dir, "record.json"),
		fibbyUnit: "spinclass-keyholder-fibby-" + h + ".service",
		agentUnit: "spinclass-keyholder-agent-" + h + ".service",
	}
}

// Start makes sure the session has a live pair and returns it. A live pair is
// left alone (no second unit, no second mint). A recorded pair with a unit gone
// is torn down and replaced, and the new key is logged as new.
func (l *Launcher) Start(ctx context.Context, sessionKey string) (Pair, error) {
	if sessionKey == "" {
		return Pair{}, errors.New("keyholder: empty session key")
	}
	unlock, err := l.lockSession(ctx, sessionKey)
	if err != nil {
		return Pair{}, err
	}
	defer unlock()
	return l.start(ctx, sessionKey)
}

func (l *Launcher) start(ctx context.Context, sessionKey string) (Pair, error) {
	ly := l.layoutFor(sessionKey)
	for _, p := range []string{ly.ctl, ly.pcsc, ly.agent} {
		if len(p) > MaxSocketPath {
			return Pair{}, fmt.Errorf("keyholder: socket path %q is %d bytes, over the %d-byte limit: set a shorter XDG_STATE_HOME", p, len(p), MaxSocketPath)
		}
	}
	if rec, ok := readRecord(ly.record); ok {
		live, err := l.pairLive(ctx, ly)
		if err != nil {
			return Pair{}, err
		}
		if live {
			rec.Dir, rec.AgentSock = ly.dir, ly.agent
			// A pair launched before the certificate directory existed gets one, and
			// the state root, the session directory and that directory are exactly 0700.
			for _, d := range []string{l.Base, ly.dir, ly.certDir} {
				if err := mkdirExact0700(d); err != nil {
					return Pair{}, fmt.Errorf("keyholder: %w", err)
				}
			}
			return rec, nil
		}
		l.logf("keyholder: the pair for %s is gone (a unit is not running): tearing it down and minting a NEW key", sessionKey)
		if err := l.teardown(ctx, ly); err != nil {
			return Pair{}, fmt.Errorf("keyholder: clearing a lost pair: %w", err)
		}
	} else if _, err := os.Lstat(ly.dir); err == nil {
		// Leftovers with no record (a start that died half way): clear them.
		l.logf("keyholder: clearing leftovers of an unfinished start for %s", sessionKey)
		if err := l.teardown(ctx, ly); err != nil {
			return Pair{}, fmt.Errorf("keyholder: clearing leftovers: %w", err)
		}
	}
	pair, err := l.launch(ctx, sessionKey, ly)
	if err != nil {
		if terr := l.teardown(context.WithoutCancel(ctx), ly); terr != nil {
			err = errors.Join(err, fmt.Errorf("tearing down after the failure: %w", terr))
		}
		return Pair{}, err
	}
	pair.Created = true
	return pair, nil
}

func (l *Launcher) launch(ctx context.Context, sessionKey string, ly layout) (Pair, error) {
	if err := os.MkdirAll(filepath.Dir(l.Base), 0o700); err != nil {
		return Pair{}, err
	}
	// The root, the session directory and the certificate directory are exactly
	// 0700 whatever the umask: the signer that installs a certificate checks the
	// directory it writes into, and so does the unit's read-only bind.
	for _, d := range []string{l.Base, ly.dir, ly.certDir} {
		if err := mkdirExact0700(d); err != nil {
			return Pair{}, err
		}
	}
	for _, d := range []string{ly.fibbyDir, ly.agentDir, ly.workDir} {
		if err := mkdirOwnerOnly(d); err != nil {
			return Pair{}, err
		}
	}
	pcscEnv := "PCSCLITE_CSOCK_NAME=" + ly.pcsc
	if err := l.Run.Start(ctx, ly.fibbyUnit,
		[]string{l.Tools.Fibby, "--socket", ly.pcsc, "--control-socket", ly.ctl, "--model", "yk5.7"}, nil); err != nil {
		return Pair{}, fmt.Errorf("starting fibby: %w", err)
	}
	if err := l.waitSocket(ctx, ly.ctl); err != nil {
		return Pair{}, fmt.Errorf("fibby's control socket: %w", err)
	}
	if err := l.waitSocket(ctx, ly.pcsc); err != nil {
		return Pair{}, fmt.Errorf("fibby's card socket: %w", err)
	}
	// fibby starts with a default virtual card; the holder mints its own.
	_, _ = l.Run.Output(ctx, []string{l.Tools.Fibby, "ctl", "--socket", ly.ctl, "destroy", "Virtual PCD piggy fibby 00 00"}, nil)
	if err := l.Run.Start(ctx, ly.agentUnit,
		[]string{l.Tools.Piggy, "agent", "--holder", "--holder-fibby-ctl", ly.ctl, "--scope", Scope + "=any", "-a", ly.agent},
		[]string{pcscEnv}); err != nil {
		return Pair{}, fmt.Errorf("starting the holder: %w", err)
	}
	if err := l.waitSocket(ctx, ly.agent); err != nil {
		return Pair{}, fmt.Errorf("the holder's agent socket: %w", err)
	}
	out, err := l.Run.Output(ctx, []string{l.Tools.Piggy, "holder", "mint", "--scope", Scope},
		[]string{"SSH_AUTH_SOCK=" + ly.agent, pcscEnv})
	if err != nil {
		return Pair{}, fmt.Errorf("minting the holder key: %w", err)
	}
	pub, err := parsePublicKey(out)
	if err != nil {
		return Pair{}, fmt.Errorf("minting the holder key: %w", err)
	}
	fp, err := Fingerprint(pub)
	if err != nil {
		return Pair{}, fmt.Errorf("minting the holder key: %w", err)
	}
	pair := Pair{
		Session: sessionKey, FibbyUnit: ly.fibbyUnit, AgentUnit: ly.agentUnit,
		PublicKey: pub, Fingerprint: fp, MintedAt: time.Now().UTC(), Dir: ly.dir, AgentSock: ly.agent,
	}
	if err := writeRecord(ly.record, pair); err != nil {
		return Pair{}, err
	}
	rm, err := l.noteMint(sessionKey, pair)
	if err != nil {
		return Pair{}, err
	}
	pair.Reminted = rm
	if rm != nil {
		l.logf("keyholder: the key of %s CHANGED: old %s, new %s: any certificate for the old key is void", sessionKey, rm.OldFingerprint, rm.NewFingerprint)
	}
	l.logf("keyholder: started a pair for %s (%s)", sessionKey, fp)
	return pair, nil
}

// Stop ends a session's pair: both units are stopped and seen inactive before
// any directory is removed. A unit that will not stop leaves the directories in
// place and is an error. A session with no pair is not an error.
func (l *Launcher) Stop(ctx context.Context, sessionKey string) error {
	h, err := l.acquire(ctx, Hash(sessionKey), sessionKey, lockWait)
	if err != nil {
		return err
	}
	if err := l.stop(ctx, sessionKey); err != nil {
		h.Unlock() // the pair is still there: keep the lock file with it
		return err
	}
	h.Remove() // the pair is gone: so is its lock file, the very last thing removed
	return nil
}

func (l *Launcher) stop(ctx context.Context, sessionKey string) error {
	ly := l.layoutFor(sessionKey)
	if err := l.teardown(ctx, ly); err != nil {
		return err
	}
	// Stop is the session's own end (close, or a launch that failed after it made
	// the pair): it ends the session's key history. A pair that was merely lost, or
	// that the operator stopped while the session lives on (StopFoundWith with
	// KeepHistory), keeps it, so the next mint is known to be a change.
	if err := os.Remove(LastPath(l.Base, sessionKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the key history of %s: %w", sessionKey, err)
	}
	// The session's kind record ends with the session, like its key history.
	if err := ForgetKind(l.Base, sessionKey); err != nil {
		return err
	}
	l.logf("keyholder: stopped the pair for %s", sessionKey)
	return nil
}

// Running reports whether the session has a recorded pair whose units are both
// running, and returns it.
func (l *Launcher) Running(ctx context.Context, sessionKey string) (Pair, bool, error) {
	ly := l.layoutFor(sessionKey)
	rec, ok := readRecord(ly.record)
	if !ok {
		return Pair{}, false, nil
	}
	live, err := l.pairLive(ctx, ly)
	if err != nil || !live {
		return Pair{}, false, err
	}
	rec.Dir, rec.AgentSock = ly.dir, ly.agent
	return rec, true, nil
}

func (l *Launcher) pairLive(ctx context.Context, ly layout) (bool, error) {
	for _, u := range []string{ly.fibbyUnit, ly.agentUnit} {
		act, err := l.Run.Active(ctx, u)
		if err != nil {
			return false, err
		}
		if !act {
			return false, nil
		}
	}
	fi, err := os.Lstat(ly.agent)
	return err == nil && fi.Mode()&os.ModeSocket != 0, nil
}

// teardown stops the agent first (it holds fibby's directory lock), then
// fibby, waits for both to be inactive, and only then removes the directories.
func (l *Launcher) teardown(ctx context.Context, ly layout) error {
	for _, u := range []string{ly.agentUnit, ly.fibbyUnit} {
		if err := l.Run.Stop(ctx, u); err != nil {
			return fmt.Errorf("stopping %s: %w", u, err)
		}
	}
	deadline := time.Now().Add(l.wait())
	for _, u := range []string{ly.agentUnit, ly.fibbyUnit} {
		for {
			act, err := l.Run.Active(ctx, u)
			if err != nil {
				return err
			}
			if !act {
				break
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				return fmt.Errorf("%s is still running: its directories are left in place", u)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := os.RemoveAll(ly.dir); err != nil {
		return fmt.Errorf("removing %s: %w", ly.dir, err)
	}
	return nil
}

func (l *Launcher) waitSocket(ctx context.Context, path string) error {
	deadline := time.Now().Add(l.wait())
	for {
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never appeared", path)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// mkdirExact0700 makes dir a real directory of this user with mode EXACTLY
// 0700: created if absent, chmod'ed after the mkdir so the umask cannot leave it
// narrower, and an existing one of ours with another mode is set to 0700. A link
// or a directory of another user is refused.
func mkdirExact0700(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (a link or a file is refused)", dir)
	}
	if !ownedByUs(fi) {
		return fmt.Errorf("%s is not owned by this user", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// mkdirOwnerOnly creates dir with mode 0700, or checks an existing one is a
// real directory of this user with no group or other access.
func mkdirOwnerOnly(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has mode %o: the holder refuses a socket directory open to anyone but its owner", dir, fi.Mode().Perm())
	}
	if !ownedByUs(fi) {
		return fmt.Errorf("%s is not owned by this user", dir)
	}
	return nil
}

func parsePublicKey(out []byte) (string, error) {
	line := strings.TrimSpace(string(out))
	if line == "" || strings.ContainsAny(line, "\n\r") {
		return "", fmt.Errorf("expected one public key line, got %q", truncate(line, 80))
	}
	f := strings.Fields(line)
	if len(f) < 2 || !strings.HasPrefix(f[0], "ssh-") && !strings.HasPrefix(f[0], "ecdsa-") && !strings.HasPrefix(f[0], "sk-") {
		return "", fmt.Errorf("not a public key line: %q", truncate(line, 80))
	}
	return line, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func readRecord(path string) (Pair, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Pair{}, false
	}
	var p Pair
	if json.Unmarshal(b, &p) != nil || p.PublicKey == "" || p.FibbyUnit == "" || p.AgentUnit == "" {
		return Pair{}, false
	}
	if p.Fingerprint == "" {
		p.Fingerprint, _ = Fingerprint(p.PublicKey)
	}
	return p, true
}

func writeRecord(path string, p Pair) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
