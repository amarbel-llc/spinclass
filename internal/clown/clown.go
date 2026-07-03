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
// $RINGMASTER_BIN overrides — for tests, or a future pin of the job platform
// once it is extracted into its own lightweight flake — otherwise a bare name
// resolves ringmaster from PATH, where it ships alongside clown. spinclass
// deliberately does NOT derive this from $CLOWN_BIN, nor pin clown as a flake
// input for it: pinning clown would drag its whole input closure in for two
// small Go binaries, and the platform is moving to a standalone repo.
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
