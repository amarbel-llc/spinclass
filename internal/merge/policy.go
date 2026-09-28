package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// AttestationGate is how the caller of a merge satisfied — or did not need to
// satisfy — the [[pre-merge-skills]] attestation gate (FDR 0007), and so what
// FinishMerge's pre-merge policy stage (FDR 0031) does before the hook. It only
// matters while the gate is live (the session hierarchy declares skills); a
// dormant gate emits no policy point on any path.
type AttestationGate int

const (
	// GateTerminal is the zero value: a terminal `sc merge` / `sc run`, which is
	// always exempt from attestation by operator decision (2026-09-28, #326).
	// The stage only records the bypass; predicates are never run, so nothing a
	// plugin declares can change a terminal merge's outcome.
	GateTerminal AttestationGate = iota
	// GateAttested: an MCP merge whose caller consumed a fresh attestation.
	GateAttested
	// GateNeedsExemption: an MCP merge with NO attestation, admitted only
	// because exemptions may apply. A [[pre-merge-exemptions]] predicate
	// resolved from the merge base's tree must vouch for the landing diff, or
	// the merge fails here — before the hook, with nothing landed.
	GateNeedsExemption
)

// ErrAttestationNotExempt is returned when a GateNeedsExemption merge finds no
// predicate that exempts its diff.
var ErrAttestationNotExempt = errors.New("pre-merge skill attestation missing and no [[pre-merge-exemptions]] predicate exempted this diff")

// ExemptWorktreePrefix names the transient detached worktree at the merge base
// that exemption predicates run in: ".exempt-<branch>-<shortsha>-<pid>".
const ExemptWorktreePrefix = ".exempt-"

// exemptionTimeout bounds all of a merge's predicates together. A predicate
// is meant to be a cheap git-object inspection; one that hangs must not wedge
// the landing lock.
const exemptionTimeout = 5 * time.Minute

// PolicyTerminalBypassReason is the SKIP reason a terminal merge records when
// the gate is live (#326): informational, never a failure.
const PolicyTerminalBypassReason = "attestation bypassed (terminal): sc merge / sc run are exempt from [[pre-merge-skills]] by policy"

const policyLabel = "pre-merge policy"

// runAttestationPolicy is the pre-merge policy stage (FDR 0031). sessionH is
// the session worktree's hierarchy (it decides whether the gate is live);
// targetRef is the landing target the landing sits on; landingSha is the head
// to judge. Only a GateNeedsExemption merge can fail here.
func runAttestationPolicy(ctx context.Context, ts *crap.TestStream, gate AttestationGate, sessionH sweatfile.Hierarchy, repoPath, branch, defaultBranch, targetRef, pinnedSha, landingSha string) error {
	// GateNeedsExemption never skips: the gate was found live at admission,
	// and the session hierarchy is branch-controlled (a queued merge's branch
	// can drop its skills or break its sweatfile before dequeue), so letting it
	// switch the gate off here would fail open. (An unloadable hierarchy is the
	// zero value: no skills.)
	if len(sessionH.Merged.ActivePreMergeSkills()) == 0 && gate != GateNeedsExemption {
		return nil
	}
	switch gate {
	case GateTerminal:
		ts.Skip(policyLabel, PolicyTerminalBypassReason)
		return nil
	case GateAttested:
		ts.Ok(policyLabel + ": attested")
		return nil
	}

	base, err := git.Run(repoPath, "merge-base", targetRef, landingSha)
	if err != nil {
		return failStep(ts, policyLabel, fmt.Errorf("%w: could not resolve merge base of %s and %s: %v", ErrAttestationNotExempt, targetRef, shortSha(landingSha), err), "")
	}
	basePath, cleanup, err := addTransientWorktree(repoPath, ExemptWorktreePrefix, branch, base)
	if err != nil {
		return failStep(ts, policyLabel, fmt.Errorf("%w: %v", ErrAttestationNotExempt, err), "")
	}
	defer cleanup()

	// Trust rule: the predicates' DEFINITION comes from the base tree (the
	// worktree layer of the hierarchy is the base checkout, never the branch),
	// and their CODE runs with cwd in it, so a branch cannot vouch for itself.
	var exemptions []sweatfile.PreMergeExemption
	if home, _ := os.UserHomeDir(); home != "" {
		if h, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, basePath); hErr == nil {
			exemptions = h.Merged.ActivePreMergeExemptions()
		}
	}

	env := []string{
		"WORKTREE=" + basePath,
		"SPINCLASS_MERGE_BASE=" + base,
		"SPINCLASS_LANDING_SHA=" + landingSha,
		"SPINCLASS_PINNED_SHA=" + pinnedSha,
		"SPINCLASS_MERGED_BRANCH=" + branch,
		"SPINCLASS_DEFAULT_BRANCH=" + defaultBranch,
		"SPINCLASS_REPO_PATH=" + repoPath,
	}
	// One deadline for ALL predicates, not one each: under the queue this is
	// lock time, so N predicates must not hold the lock N times the cap.
	ctx, cancel := context.WithTimeout(ctx, exemptionTimeout)
	defer cancel()
	var declined []string
	for _, e := range exemptions {
		verdict, out := runExemption(ctx, e, basePath, env)
		if verdict == "" {
			ts.Ok(fmt.Sprintf("%s: exempt (%s) base=%s landing=%s", policyLabel, e.Name, shortSha(base), shortSha(landingSha)))
			return nil
		}
		declined = append(declined, e.Name+" ("+verdict+")")
		if out != "" {
			declined[len(declined)-1] += ": " + tail(out, 400)
		}
	}

	msg := "no predicate is declared at the merge base"
	if len(declined) > 0 {
		msg = "declined: " + strings.Join(declined, "; ")
	}
	return failStep(ts, policyLabel, fmt.Errorf("%w (%s); call `nothing-but-the-truth` to attest, then re-merge", ErrAttestationNotExempt, msg), "")
}

// runExemption runs one predicate via sh -c in dir with the merging user's
// inherited environment plus env — deliberately WITHOUT the devshell, which is
// head-controlled. It returns "" when the predicate exempts (exit 0), else a
// verdict ("exit N", "timeout", "spawn-failed: …") and the captured output.
func runExemption(ctx context.Context, e sweatfile.PreMergeExemption, dir string, env []string) (verdict, output string) {
	cmd := exec.CommandContext(ctx, "sh", "-c", sweatfile.NormalizeCommand(e.Command))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(env, "SPINCLASS_EXEMPTION="+e.Name)...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	output = strings.TrimSpace(buf.String())
	switch {
	case err == nil:
		return "", output
	case ctx.Err() == context.DeadlineExceeded:
		return "timeout", output
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "exit " + strconv.Itoa(exitErr.ExitCode()), output
	}
	return "spawn-failed: " + err.Error(), output
}

// addTransientWorktree creates a detached worktree at sha named
// <prefix><branch>-<shortsha>-<pid> under <repo>/.worktrees/ (the shape
// sc clean's orphan reaper parses) and returns its path plus an idempotent
// cleanup that force-removes it and prunes admin entries. Shared by the
// landing (.land-*) and exemption (.exempt-*) worktrees.
func addTransientWorktree(repoPath, prefix, branch, sha string) (string, func(), error) {
	noop := func() {}
	parent := filepath.Join(repoPath, ".worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", noop, fmt.Errorf("create worktree parent %s: %w", parent, err)
	}
	path := filepath.Join(parent, prefix+strings.ReplaceAll(branch, "/", "-")+"-"+shortSha(sha)+"-"+strconv.Itoa(os.Getpid()))
	// Clear a stale physical dir from an interrupted prior run (same guard,
	// same rationale as check.resolveHookDir).
	if err := os.RemoveAll(path); err != nil {
		return "", noop, fmt.Errorf("remove stale worktree dir %s: %w", path, err)
	}
	if err := git.WorktreeAddDetached(repoPath, path, sha); err != nil {
		return "", noop, fmt.Errorf("create worktree at %s: %w", path, err)
	}
	removed := false
	return path, func() {
		if removed {
			return
		}
		removed = true
		_ = git.WorktreeForceRemove(repoPath, path)
		_ = git.WorktreePrune(repoPath)
	}, nil
}

// tail keeps the last n bytes of s, so a chatty predicate cannot flood the
// verdict line.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
