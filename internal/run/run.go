// Package run implements `sc run` — a single non-interactive primitive that
// starts a worktree session, runs ONE command sequence inside it (the same
// devshell + SPINCLASS_* identity path as `sc exec`), then merges + tears the
// session down. It collapses the hand-assembled start → exec → merge → close
// lifecycle (#194) into one call, removing the TAP-grepping key capture and
// the conditional-cleanup footguns.
//
// The success-path teardown is a 2×2 matrix over two orthogonal flags:
//
//	(default)             merge into the default branch; session torn down
//	--no-close            merge into the default branch; worktree/session left
//	--no-merge            skip merge; close ONLY if no commits were produced
//	--no-merge --no-close skip merge; worktree/session left intact
//
// An empty run (no commits ahead of the default branch) is a clean success,
// not a failure. Any step that exits nonzero leaves the worktree + session
// intact for inspection and propagates a nonzero exit code (flags ignored on
// the failure path) — matching spawn's hello-timeout "leave for inspection"
// convention. `sc clean` does not reap it: the worktree is present and the
// state is inactive, not abandoned.
package run

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"github.com/mattn/go-isatty"

	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/present"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sessionexec"
	"code.linenisgreat.com/spinclass/internal/shop"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// Spec is the fully-parsed `sc run` invocation. Exactly one of Util / Script
// is non-empty (ParseArgs enforces this).
type Spec struct {
	Description    string
	NoMerge        bool
	NoClose        bool
	LocalOnly      bool
	AllowStaleBase bool     // --allow-stale-base: create even from an unverified default branch
	Format         string   // global --format value (auto/viewport/plain/ndjson)
	Util           []string // `-- <util> [args...]` form
	Script         []byte   // stdin-script form (shebang-aware)
}

// Run executes the full lifecycle and returns the process exit code to
// propagate (0 success; nonzero on a step or merge failure). A non-nil error
// is returned only for setup failures that aren't a step verdict (repo
// detection, worktree creation, default-branch resolution) — the caller
// surfaces it directly rather than via the crap stream.
func Run(spec Spec) (exitCode int, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	repoPath, err := worktree.DetectRepo(cwd)
	if err != nil {
		return 1, err
	}

	var descArgs []string
	if spec.Description != "" {
		descArgs = []string{spec.Description}
	}
	rp, err := worktree.ResolvePath(repoPath, descArgs)
	if err != nil {
		return 1, err
	}

	resolvedFmt, err := present.ResolveFormat(spec.Format, isatty.IsTerminal(os.Stdout.Fd()))
	if err != nil {
		return 1, err
	}

	// Resolve the default branch BEFORE the reporter scope. It is the base for
	// the commits-ahead count (which drives both the nothing-to-merge skip and
	// the --no-merge close-vs-leave decision) AND the merge target. When
	// merging, ResolveDefaultBranch may huh-prompt on an ambiguous main/master,
	// which a TUI cannot nest inside the live viewport — and on a non-TTY would
	// hang — so fail fast there. When --no-merge, resolve non-interactively
	// (git.DefaultBranch never prompts); an ambiguous repo just means we cannot
	// count commits precisely, so we conservatively treat the run as having
	// produced work (leave the session) by leaving defaultBranch empty.
	var defaultBranch string
	if spec.NoMerge {
		defaultBranch, _ = git.DefaultBranch(repoPath) // "" on ambiguity/err — handled below
	} else {
		if !isatty.IsTerminal(os.Stdin.Fd()) {
			if _, derr := git.DefaultBranch(repoPath); errors.Is(derr, git.ErrAmbiguousDefaultBranch) {
				return 1, errors.New("ambiguous default branch (main vs master) and stdin is not a TTY: cannot prompt; pass --no-merge or disambiguate the repo")
			}
		}
		defaultBranch, err = merge.ResolveDefaultBranch(repoPath)
		if err != nil {
			return 1, err
		}
	}

	// Create the worktree and write findable inactive session state. Reusing
	// shop.Attach with noAttach=true is the create-then-record path the
	// companion --no-attach change established (FDR 0014 / #194): it applies
	// the worktree setup once and writes inactive/PID-0 state without exec'ing
	// an entrypoint. format="" + io.Discard keeps its own output off the crap
	// stream (the reporter owns stdout).
	hierarchy, herr := sweatfileio.LoadWorktreeHierarchy(os.Getenv("HOME"), repoPath, rp.AbsPath)
	if herr != nil {
		hierarchy, herr = sweatfileio.LoadHierarchy(os.Getenv("HOME"), repoPath)
		if herr != nil {
			return 1, herr
		}
	}
	merged := hierarchy.Merged
	sexec := executor.SessionExecutor{
		Entrypoint:  merged.SessionStart(),
		Description: rp.Description,
		Env:         merged.SessionEnv(),
	}
	if aerr := shop.Attach(io.Discard, sexec, rp, merged, "", false, true /*noAttach*/, false, spec.AllowStaleBase); aerr != nil {
		return 1, aerr
	}

	st, rerr := session.Read(rp.RepoPath, rp.Branch)
	if rerr != nil {
		return 1, fmt.Errorf("read session state after create: %w", rerr)
	}

	// step / merge verdicts share one continuous reporter scope. The numeric
	// exit code is stashed in a closure var (the reporter callback returns an
	// error, not a code); we distinguish step-fail vs merge-fail vs success
	// outside the scope to drive teardown.
	var (
		stepFailed  bool
		stepCode    int
		mergeFailed bool
		didMerge    bool
		aheadN      = -1 // -1 = unknown (ambiguous default branch under --no-merge)
	)

	repErr := present.WithReporter(resolvedFmt, "run "+rp.Branch, os.Stdout, os.Stderr, func(rep *crap.Reporter) error {
		ts := rep.TestStream(0)
		defer ts.Finish()

		// --- the single step ---
		stepErr := runStep(rep, st, spec)
		if stepErr != nil {
			stepFailed = true
			stepCode = exitCodeFromErr(stepErr)
			return stepErr
		}

		if defaultBranch != "" {
			aheadN = git.CommitsAhead(rp.AbsPath, defaultBranch, rp.Branch)
		}

		// --- merge (unless --no-merge) ---
		if spec.NoMerge {
			return nil
		}
		if aheadN == 0 {
			// Empty run: skip the (failing) nothing-to-merge guard and report a
			// clean success. The worktree was NOT removed (no merge ran), so
			// teardown must close it like the --no-merge aheadN==0 path.
			ts.Ok("nothing to merge (skipped)")
			return nil
		}
		// inSession=spec.NoClose: --no-close leaves the worktree + branch +
		// session in place (merge skips its teardown when inSession), so the
		// session keeps accumulating like a normal sc worktree. The default
		// (inSession=false) lets the merge remove the worktree + branch; the
		// dangling index entry is then dropped in teardown.
		if _, mErr := merge.Resolved(executor.ShellExecutor{}, rep, ts,
			rp.RepoPath, rp.AbsPath, rp.Branch, defaultBranch, !spec.LocalOnly, spec.NoClose); mErr != nil {
			mergeFailed = true
			return mErr
		}
		didMerge = true
		return nil
	})

	// Failure path: leave the worktree + session intact for inspection.
	if stepFailed {
		return nonzero(stepCode), nil
	}
	if mergeFailed {
		return 1, nil
	}
	if repErr != nil {
		// A renderer error with no step/merge failure — surface it.
		return 1, repErr
	}

	// --- success teardown (the 2×2 matrix) ---
	if err := teardown(rp, spec, didMerge, aheadN); err != nil {
		// Teardown is best-effort: the work already succeeded. Warn, exit 0.
		fmt.Fprintf(os.Stderr, "sc run: teardown: %v\n", err)
	}
	return 0, nil
}

// runStep runs the single command/script as a crap Phase, teeing its combined
// output into the phase via a LineWriter. Returns the command's error (an
// *exec.ExitError carrying the code) or nil on success.
func runStep(rep *crap.Reporter, st *session.State, spec Spec) error {
	util, label, cleanup, err := stepCommand(st, spec)
	if err != nil {
		return err
	}
	if cleanup != nil {
		// Keep the temp script for inspection on failure; remove on success.
		defer func() {
			if err == nil {
				cleanup()
			}
		}()
	}

	ph := rep.Phase("run step")
	ph.Command(label)
	lw := present.NewLineWriter(ph)
	defer lw.Flush()

	cmd := sessionexec.CommandIn(st.WorktreePath, util, sessionexec.IdentityEnv(st))
	cmd.Stdout = lw
	cmd.Stderr = lw
	if err = cmd.Run(); err != nil {
		lw.Flush()
		ph.FailDiag(err, map[string]any{
			"severity":  "fail",
			"message":   err.Error(),
			"command":   label,
			"exit_code": exitCodeFromErr(err),
		})
		return err
	}
	ph.Done()
	return nil
}

// stepCommand resolves the Spec into the argv to exec and a display label.
// For the stdin-script form it materializes a shebang-aware temp script under
// the worktree's .tmp and returns a cleanup func (nil for the --util form).
func stepCommand(st *session.State, spec Spec) (util []string, label string, cleanup func(), err error) {
	if len(spec.Util) > 0 {
		return spec.Util, strings.Join(spec.Util, " "), nil, nil
	}

	interp, body := splitShebang(spec.Script)
	tmpDir := filepath.Join(st.WorktreePath, ".tmp")
	if mkErr := os.MkdirAll(tmpDir, 0o755); mkErr != nil {
		return nil, "", nil, fmt.Errorf("create .tmp for run script: %w", mkErr)
	}
	f, cErr := os.CreateTemp(tmpDir, "sc-run-*.sh")
	if cErr != nil {
		return nil, "", nil, fmt.Errorf("write run script: %w", cErr)
	}
	path := f.Name()
	if _, wErr := f.Write(body); wErr != nil {
		_ = f.Close()
		return nil, "", nil, fmt.Errorf("write run script: %w", wErr)
	}
	if cErr := f.Close(); cErr != nil {
		return nil, "", nil, fmt.Errorf("write run script: %w", cErr)
	}
	if chErr := os.Chmod(path, 0o755); chErr != nil {
		return nil, "", nil, fmt.Errorf("chmod run script: %w", chErr)
	}

	util = append(append([]string{}, interp...), path)
	label = strings.Join(interp, " ") + " <stdin script>"
	cleanup = func() { _ = os.Remove(path) }
	return util, label, cleanup, nil
}

// splitShebang inspects a stdin script: if line 1 is a `#!` shebang, the
// interpreter (and its inline args) is parsed off it and the FULL script —
// shebang line included — is returned as the body (so the script is run as a
// regular executable file: `interp path`). Without a shebang the interpreter
// defaults to `sh` and the body is the script verbatim.
func splitShebang(script []byte) (interp []string, body []byte) {
	if bytes.HasPrefix(script, []byte("#!")) {
		nl := bytes.IndexByte(script, '\n')
		first := script
		if nl >= 0 {
			first = script[:nl]
		}
		fields := strings.Fields(strings.TrimPrefix(string(first), "#!"))
		if len(fields) > 0 {
			return fields, script
		}
	}
	return []string{"sh"}, script
}

// teardown applies the success-path 2×2 matrix. It runs only after a
// successful run; the failure path returns before reaching here. merged
// reports whether a merge actually executed (it removes the worktree + branch);
// aheadN is the commit count the run produced (-1 = unknown).
func teardown(rp worktree.ResolvedPath, spec Spec, didMerge bool, aheadN int) error {
	if spec.NoClose {
		// Leave the session as-is. Under --no-close the merge ran with
		// inSession=true, so it left the worktree + branch + session in place —
		// nothing to tear down.
		return nil
	}

	if didMerge {
		// The merge ran with inSession=false and removed the worktree + branch;
		// drop the dangling index entry it leaves behind (it does not tombstone).
		return session.Remove(rp.RepoPath, rp.Branch)
	}

	// No merge ran (either --no-merge, or an empty run that skipped the merge).
	// The worktree is still present. Never silently discard committed work:
	// close only when the run is known to have produced no commits (aheadN == 0);
	// aheadN != 0 covers both "made commits" and "unknown" (-1, ambiguous
	// default branch under --no-merge) — leave the session intact for inspection.
	if aheadN != 0 {
		fmt.Fprintf(os.Stderr,
			"sc run: run may have produced commits (ahead=%d); leaving session %s for inspection\n",
			aheadN, rp.SessionKey)
		return nil
	}
	return closeSession(rp)
}

// closeSession force-removes the worktree, deletes the branch, and drops the
// session state — the non-interactive equivalent of `sc close --force`.
func closeSession(rp worktree.ResolvedPath) error {
	if err := git.WorktreeForceRemove(rp.RepoPath, rp.AbsPath); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	if _, err := git.BranchDelete(rp.RepoPath, rp.Branch); err != nil {
		// -d refuses an unmerged branch; with 0 commits ahead it is merged, so
		// a failure here is unexpected — surface it but don't fail the run.
		return fmt.Errorf("delete branch: %w", err)
	}
	return session.Remove(rp.RepoPath, rp.Branch)
}

// exitCodeFromErr extracts a process exit code from an *exec.ExitError.
// Returns 0 for nil, -1 for a non-ExitError error (mirrors check.exitCodeFromErr).
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// nonzero maps a possibly-zero/negative exit code to a guaranteed-nonzero one
// so a failed step never reports success to the shell.
func nonzero(code int) int {
	if code <= 0 {
		return 1
	}
	return code
}

// ParseArgs splits the passthrough argv of `sc run` into a Spec. Grammar:
//
//	[--description D | -d D | --description=D] [--no-merge] [--no-close]
//	[--local-only] [--allow-stale-base] [--format F | --format=F]
//	( -- <util> [args...] | <stdin script> )
//
// Flags are hand-parsed (the command uses PassthroughArgs, like `sc exec`) up
// to a `--`; everything after `--` is the util argv. With no `--`, a script is
// read from stdin. The two forms are mutually exclusive and at least one must
// be present.
func ParseArgs(args []string, stdin io.Reader) (Spec, error) {
	var spec Spec
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			spec.Util = args[i:]
			break
		}
		switch {
		case a == "--no-merge":
			spec.NoMerge = true
			i++
		case a == "--no-close":
			spec.NoClose = true
			i++
		case a == "--local-only":
			spec.LocalOnly = true
			i++
		case a == "--allow-stale-base":
			spec.AllowStaleBase = true
			i++
		case a == "--description" || a == "-d":
			if i+1 >= len(args) {
				return spec, fmt.Errorf("%s requires a value", a)
			}
			spec.Description = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--description="):
			spec.Description = strings.TrimPrefix(a, "--description=")
			i++
		case a == "--format":
			if i+1 >= len(args) {
				return spec, fmt.Errorf("%s requires a value", a)
			}
			spec.Format = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--format="):
			spec.Format = strings.TrimPrefix(a, "--format=")
			i++
		default:
			return spec, fmt.Errorf("unknown flag or stray argument %q (a command must follow `--`)", a)
		}
	}

	if len(spec.Util) > 0 {
		return spec, nil
	}

	// No `-- util`: read a script from stdin.
	if stdin != nil {
		body, err := io.ReadAll(bufio.NewReader(stdin))
		if err != nil {
			return spec, fmt.Errorf("read stdin script: %w", err)
		}
		if len(bytes.TrimSpace(body)) > 0 {
			spec.Script = body
			return spec, nil
		}
	}
	return spec, errors.New("nothing to run: pass a command after `--` or pipe a script on stdin")
}
