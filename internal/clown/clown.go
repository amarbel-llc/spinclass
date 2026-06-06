// Package clown is the producer-side integration with clown's job-wakeup
// channel (clown RFC-0009): chat message wakes and background-job lifecycle
// events, emitted via the clown CLI. The on-disk journal clown maintains is
// the wake layer only — spinclass state (chatroom store, job.json) remains
// the system of record for every consumer of this package.
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

// Bin resolves the clown binary to invoke: $CLOWN_BIN (exported by clown into
// every plugin MCP server, RFC-0009 §2) with a PATH-lookup fallback.
func Bin() string {
	if v := os.Getenv("CLOWN_BIN"); v != "" {
		return v
	}
	return "clown"
}

// Enabled reports whether this process may emit job-wakeup events: CLOWN_BIN
// being set is the "running under clown" signal and the producer-may-emit
// contract agreed with clown. Rollback is clown's own CLOWN_DISABLE_JOB_WAKEUP
// (emits become exit-0 no-ops), so no spinclass-side switch exists.
func Enabled() bool {
	return os.Getenv("CLOWN_BIN") != ""
}

// emitTimeout bounds each clown CLI call so a wedged binary cannot hang the
// caller. Emits are a local journal append + optional datagram; seconds is
// generous.
const emitTimeout = 10 * time.Second

// run invokes the clown CLI with args, detached from the caller's
// cancellation (the spinclass-side state write has already happened by the
// time an emit runs; the wake should still go out if the originating request
// is cancelled). Returns trimmed stdout.
func run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, Bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) > 0 {
			return "", fmt.Errorf("clown %s: %w: %s", args[0], err, detail)
		}
		return "", fmt.Errorf("clown %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// SendMessage emits a chat `message` waking event addressed to target (a
// session key, or clown's reserved broadcast key "*"). Clown flattens
// newlines in body; pass it raw.
func SendMessage(ctx context.Context, target, from, source, body, resultRef string) error {
	_, err := run(
		ctx,
		"job", "message",
		"--target", target,
		"--from", from,
		"--source", source,
		"--message", body,
		"--result-ref", resultRef,
	)
	return err
}

// StartJob allocates a clown job (journal-only `started` record, no wake) and
// returns its id for the matching FinishJob.
func StartJob(ctx context.Context, label, source string) (string, error) {
	id, err := run(ctx, "job", "start", "--label", label, "--source", source)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("clown job start: no job id on stdout")
	}
	return id, nil
}

// FinishJob appends the job's terminal record (state is one of clown
// RFC-0009 §5's terminal types), waking the target session. Empty message and
// resultRef are omitted.
func FinishJob(ctx context.Context, id, state, message, resultRef string) error {
	args := []string{"job", "done", id, "--state", state}
	if message != "" {
		args = append(args, "--message", message)
	}
	if resultRef != "" {
		args = append(args, "--result-ref", resultRef)
	}
	_, err := run(ctx, args...)
	return err
}
