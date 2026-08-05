// Package clown is the producer-side integration with clown's job-wakeup
// channel (clown RFC-0009): background-job lifecycle events emitted via clown's
// ringmaster job-control CLI. clown RFC-0015 split the former `clown job`
// subcommands into standalone binaries — `ringmaster` (job control) and
// `troupe` (messaging) — shipped alongside clown on PATH. spinclass drives only
// ringmaster: cross-session chat left spinclass for clown's troupe MCP tools
// (FDR 0017), so there is no troupe shell-out here. The on-disk journal clown
// maintains is the wake layer only — spinclass state (job.json) remains the
// system of record for every consumer of this package.
package clown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// Source is the producer label spinclass stamps on every emitted event
// (RFC-0009 `source` field): it identifies the emitting plugin in the
// notification line.
const Source = "spinclass"

// ringmasterBin resolves clown's ringmaster job-control CLI (clown RFC-0015).
// $RINGMASTER_BIN overrides — used by the contract tests — otherwise a bare
// name resolves ringmaster from PATH. spinclass deliberately does NOT derive
// this from $CLOWN_BIN: the two are separate binaries, and clown's presence is
// the emit GATE (see Enabled), not the way ringmaster is located.
//
// Resolution stays PATH-based at run time even though the job platform is now
// pinnable. FDR 0010 originally justified that by the cost of pinning clown
// itself; that reasoning expired when the platform was extracted into the
// standalone `ringmaster` repo, whose inputs are a strict subset of
// spinclass's. The flake now pins it as a checkPhase input so the contract can
// be tested for real (#253) — but a RUNTIME pin would be wrong for a different
// reason: the wake has to land in the journal of whatever clown is hosting
// this process, so the binary must be the one that clown ships, not one
// spinclass froze at its own build time.
func ringmasterBin() string {
	if v := os.Getenv("RINGMASTER_BIN"); v != "" {
		return v
	}
	return "ringmaster"
}

// Enabled reports whether this process may emit job-wakeup events: CLOWN_BIN
// being set is the "running under clown" signal and the producer-may-emit
// contract agreed with clown. Rollback is clown's own CLOWN_DISABLE_JOB_WAKEUP
// (emits become exit-0 no-ops), so no spinclass-side switch exists.
func Enabled() bool {
	return os.Getenv("CLOWN_BIN") != ""
}

// emitTimeout bounds each ringmaster CLI call so a wedged binary cannot hang
// the caller. Emits are a local journal append + optional datagram; seconds is
// generous.
const emitTimeout = 10 * time.Second

// run invokes the ringmaster CLI with args, detached from the caller's
// cancellation (the spinclass-side state write has already happened by the
// time an emit runs; the wake should still go out if the originating request
// is cancelled). Returns trimmed stdout.
func run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ringmasterBin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) > 0 {
			return "", fmt.Errorf("ringmaster %s: %w: %s", args[0], err, detail)
		}
		return "", fmt.Errorf("ringmaster %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// StartJob allocates a clown job (journal-only `started` record, no wake) and
// returns its id for the matching FinishJob.
func StartJob(ctx context.Context, label, source string) (string, error) {
	id, err := run(ctx, "start", "--label", label, "--source", source)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("ringmaster start: no job id on stdout")
	}
	return id, nil
}

// SpoolPath resolves the absolute path of the job's output spool — where
// ringmaster expects a producer to write the job's live output, and what
// `ringmaster status --tail` / `ringmaster tail -f` read (RFC-0015). The file
// is not created by this call.
//
// spinclass's own `.spinclass/job.log` remains the system of record for job
// output; the spool is written in addition, so ringmaster's native surface
// reports something better than the `spool_bytes: 0` it saw before
// (spinclass#251). A failure here is never fatal: the job simply runs with an
// empty spool, exactly as before.
func SpoolPath(ctx context.Context, id string) (string, error) {
	path, err := run(ctx, "spool-path", id)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("ringmaster spool-path: no path on stdout")
	}
	return path, nil
}

// FinishJob appends the job's terminal record (state is one of clown
// RFC-0009 §5's terminal types), waking the target session. Empty message and
// resultRef are omitted.
//
// resources are attached by reference via `--resource` (repeatable), which is
// how a wake carries its actual result rather than a pointer back at another
// tool (spinclass#251). Each is a URI the receiver fetches — in practice a
// `madder://blobs/<digest>` holding the rendered verdict ladder. Empty entries
// are skipped, so a caller that failed to produce a blob can pass "" without
// branching.
func FinishJob(ctx context.Context, id, state, message, resultRef string, resources ...string) error {
	args := []string{"done", id, "--state", state}
	if message != "" {
		args = append(args, "--message", message)
	}
	if resultRef != "" {
		args = append(args, "--result-ref", resultRef)
	}
	for _, r := range resources {
		if r != "" {
			args = append(args, "--resource", r)
		}
	}
	_, err := run(ctx, args...)
	return err
}

// protocolCheck memoizes the one-time comparison of the ProtocolVersion this
// binary linked jobwake at (compiled in) against the running ringmaster's
// `version --protocol`. The verdict is a process-global fact, so it is computed
// once and every later caller reads the cache.
var (
	protocolOnce sync.Once
	protocolOK   bool
	protocolWant int
	protocolGot  int
	protocolErr  error
)

// CheckProtocol compares the jobwake ProtocolVersion this build linked against
// (RFC-0018 §1, compiled in) with the integer the hosting ringmaster prints for
// `ringmaster version --protocol`, and reports whether they match EXACTLY.
//
// Memoized: the first call shells out and caches the verdict; later calls
// (serve-start gate, the sysprompt fragment, each async dispatch) read the
// cache, so the ctx of later calls is ignored — the check is a one-time global
// fact, not a per-request operation. want is jobwake.ProtocolVersion, got is
// the CLI's integer. A shell-out or parse failure returns ok=false with err
// set; the caller treats "couldn't determine" the same as a mismatch (an old
// ringmaster with no `--protocol` flag errors here, which is exactly a skew).
//
// Only meaningful under clown (the flock + wake machinery it gates is only
// used when clown.Enabled()); callers gate on that before consulting it.
func CheckProtocol(ctx context.Context) (ok bool, want, got int, err error) {
	protocolOnce.Do(func() {
		protocolWant = jobwake.ProtocolVersion
		out, rerr := run(ctx, "version", "--protocol")
		if rerr != nil {
			protocolErr = rerr
			return
		}
		n, perr := strconv.Atoi(strings.TrimSpace(out))
		if perr != nil {
			protocolErr = fmt.Errorf(
				"parsing `ringmaster version --protocol` output %q: %w", out, perr,
			)
			return
		}
		protocolGot = n
		protocolOK = n == protocolWant
	})
	return protocolOK, protocolWant, protocolGot, protocolErr
}

// flockEnabled records whether serve should hold the per-job liveness flock:
// true only after a serve-start CheckProtocol confirmed an exact
// ProtocolVersion match. job.Start reads it per dispatch WITHOUT re-shelling
// `version --protocol` — doing that would add a subprocess to every job and
// couple the job tests to the ringmaster CLI (and, because CheckProtocol is
// memoized process-globally, make the shell-out order-dependent across tests).
// Default false: a process that never ran the serve-start gate (a unit test, a
// non-serve entrypoint) holds no flock — the safe degrade.
var flockEnabled atomic.Bool

// SetFlockEnabled records the serve-start ProtocolVersion verdict. Called once
// from serve startup, before the MCP server accepts requests, so job.Start's
// reads never race the write.
func SetFlockEnabled(ok bool) { flockEnabled.Store(ok) }

// FlockEnabled reports whether job.Start should acquire the per-job liveness
// flock — the cached serve-start verdict from SetFlockEnabled, no shell-out.
func FlockEnabled() bool { return flockEnabled.Load() }

// terminalStates are the ringmaster job states meaning the job has ended
// (RFC-0009 §5 / RFC-0018 aborted). A cancel-requested job's DERIVED state
// stays "running" — the record is non-terminal — so it is deliberately absent
// here; that absence is exactly how WaitForCancel tells a cancel-requested
// return from a terminal one.
var terminalStates = map[string]bool{
	"succeeded":   true,
	"failed":      true,
	"aborted":     true,
	"interrupted": true,
}

// WaitForCancel blocks until ringmaster records either a cancel-requested or a
// terminal state for jobID, then reports whether it was a cancel-requested
// (RFC-0018). The #22 cancel observer runs this per async job and fires the
// job's context cancel when it returns true (tearing down the hook so the
// producer writes the terminal `aborted` itself).
//
// It shells `ringmaster wait <id> --on-cancel --json --timeout 0` under ctx —
// a long-lived, ctx-cancellable call, so it deliberately does NOT go through
// run(): run()'s emitTimeout would cut a legitimately long job short, and its
// WithoutCancel would ignore the observer's ctx. Cancelling ctx (the job ended
// by another path) kills the wait subprocess, surfacing as an error the caller
// treats as "nothing to observe".
//
// The signal is indirect by necessity (verified live 2026-08-04): `wait
// --on-cancel`'s status reports state "running" for a cancel-requested job —
// the derived state, since the record is non-terminal — never
// "cancel-requested". Because --on-cancel's only stop conditions are a terminal
// OR a cancel-requested, a return with a NON-terminal state means
// cancel-requested was observed. Using the CLI (not the linked
// jobwake.WaitDoneOnCancel) keeps cancel observation working across a
// ProtocolVersion skew, where the in-process flock is disabled but the runtime
// ringmaster's cancel surface still functions.
func WaitForCancel(ctx context.Context, jobID string) (cancelRequested bool, err error) {
	cmd := exec.CommandContext(ctx, ringmasterBin(),
		"wait", jobID, "--on-cancel", "--json", "--timeout", "0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if rerr := cmd.Run(); rerr != nil {
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) > 0 {
			return false, fmt.Errorf("ringmaster wait: %w: %s", rerr, detail)
		}
		return false, fmt.Errorf("ringmaster wait: %w", rerr)
	}
	var status struct {
		State string `json:"state"`
	}
	if jerr := json.Unmarshal(stdout.Bytes(), &status); jerr != nil {
		return false, fmt.Errorf(
			"parsing `ringmaster wait --json` output %q: %w",
			bytes.TrimSpace(stdout.Bytes()), jerr,
		)
	}
	return !terminalStates[status.State], nil
}

// AcquireJobLock takes ringmaster's per-job advisory lock (RFC-0016 §1) for
// jobID on the current session's channel. The lock is held by THIS process for
// the job's lifetime, so the OS releases it when serve dies — including a hard
// crash that never writes a terminal record, which is the liveness signal the
// probe reads to declare a producer gone.
//
// It is a linked-library call into jobwake, deliberately NOT a CLI shell-out:
// a short-lived `ringmaster` subprocess would drop the lock the instant it
// exits, defeating the crash guarantee. The empty target selects the current
// session, exactly the channel clown.StartJob's argument-free `start` allocated
// the job on. Returns the release closer the caller invokes on the terminal
// write, or jobwake.ErrAlreadyLocked if a live holder already holds it.
func AcquireJobLock(jobID string) (func() error, error) {
	return jobwake.AcquireJobLock("", jobID)
}

// ScopeArgv returns the `systemd-run --user --scope --unit=… --property=
// KillMode=control-group --` prefix a producer prepends to its own hook command
// to run it inside the job's transient scope (RFC-0016 §3/§4), together with
// whether the scope tier is available on this host. When the bool is false the
// caller runs the hook bare — the #26 AcquireJobLock liveness floor still
// applies on every platform. Producer-called and RFC-0016 §4.2-safe: ringmaster
// only supplies the argv, it never decides to kill and is not in the `status`
// path, so cancel and status stay platform-uniform. Availability is a cheap
// best-effort pre-check (systemd-run on PATH + a reachable user manager +
// session bus, unless RINGMASTER_DISABLE_SCOPE is set), not a guarantee — a
// spawn failure still falls back to the bare command.
func ScopeArgv(jobID string) ([]string, bool) {
	return jobwake.ScopeArgv(jobID)
}

// ScopeStop reaps a job's scope cgroup — `systemctl --user stop
// ringmaster-<job-id>.scope` — as the cancellation backstop above spinclass's
// #188 SIGTERM teardown: it guarantees a hook subtree (a detached `nix`, a
// wedged builder) is gone even if the top process swallowed SIGTERM. Returns
// ErrScopeUnavailable when the tier is off, so a caller need not pre-check; ctx
// bounds the call (the stop SIGTERMs then SIGKILLs the cgroup, up to the unit's
// stop timeout). Consumer/observer affordance only — ringmaster's own cancel
// path never calls it (RFC-0016 §4.2).
func ScopeStop(ctx context.Context, jobID string) error {
	return jobwake.ScopeStop(ctx, jobID)
}

// ScopeUnitName is the transient scope unit name a job's hook runs under
// (`ringmaster-<job-id>.scope`, RFC-0016 §3), computed in exactly one place so a
// stopper names the identical unit the producer created rather than re-spelling
// the convention.
func ScopeUnitName(jobID string) string {
	return jobwake.ScopeUnitName(jobID)
}

// jobIDKey types the context key that carries an async job's id from job.Start
// down to the pre-merge hook exec, so the hook can be wrapped in the job's scope
// (#25) without threading the id through the merge/check call chain.
type jobIDKey struct{}

// WithJobID returns ctx carrying the async job id. job.Start sets it on the
// context handed to the job function; the pre-merge hook exec reads it back via
// JobIDFromContext to scope the hook. Post-merge deliberately does NOT consult
// it — a control-group scope kill would reap the detached children FDR 0023
// sanctions for slow deploys.
func WithJobID(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, jobIDKey{}, jobID)
}

// JobIDFromContext returns the async job id set by WithJobID, or "" if none.
func JobIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(jobIDKey{}).(string)
	return id
}
