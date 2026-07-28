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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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
func FinishJob(ctx context.Context, id, state, message, resultRef string) error {
	args := []string{"done", id, "--state", state}
	if message != "" {
		args = append(args, "--message", message)
	}
	if resultRef != "" {
		args = append(args, "--result-ref", resultRef)
	}
	_, err := run(ctx, args...)
	return err
}
