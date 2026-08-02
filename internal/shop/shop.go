package shop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/mattn/go-isatty"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/basebranch"
	"code.linenisgreat.com/spinclass/internal/close"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/present"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/worktree"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

// Create ensures the worktree exists and applies its setup on first creation.
// It returns whether the worktree already existed (true ⇒ setup was NOT
// re-applied, since createWorktree only applies on a fresh worktree), so
// callers can decide whether to record a fresh setup fingerprint.
func Create(
	writer io.Writer,
	worktreePath worktree.ResolvedPath,
	opts CreateOpts,
	tapWriter *tap.Writer,
) (bool, error) {
	// The writer has to exist before createWorktree runs, since the base-branch
	// step reports through it. A self-owned writer closes with a trailing
	// Plan() rather than PlanAhead: the point count is conditional (the base
	// step only runs when a worktree is actually being created), so it is not
	// knowable up front.
	ownWriter := false
	if opts.Format == "tap" && tapWriter == nil {
		tapWriter = tap.NewWriter(writer)
		ownWriter = true
	}

	existed, err := createWorktree(worktreePath, opts, tapWriter)
	if err != nil {
		return existed, err
	}

	if opts.Format == "tap" {
		if existed {
			tapWriter.Skip("create "+worktreePath.Branch, "already exists "+worktreePath.AbsPath)
		} else {
			tapWriter.Ok("create " + worktreePath.Branch + " " + worktreePath.AbsPath)
		}
		if ownWriter {
			tapWriter.Plan()
		}
		return existed, nil
	}

	_, _ = fmt.Fprintln(writer, worktreePath.AbsPath)
	return existed, nil
}

// CreateOpts carries what Create needs beyond the resolved path.
type CreateOpts struct {
	Verbose bool
	Format  string
	// AllowStaleBase permits creating a session even when the repo's default
	// branch could not be confirmed current. The caller resolves it from
	// `--allow-stale-base` OR [hooks].allow-stale-base — two halves of one
	// override — so this is already the final answer by the time it lands here.
	AllowStaleBase bool
}

func createWorktree(worktreePath worktree.ResolvedPath, opts CreateOpts, tw *tap.Writer) (bool, error) {
	existed := true

	if _, err := os.Stat(worktreePath.AbsPath); os.IsNotExist(err) {
		existed = false

		// Freshen the default branch and resolve the base BEFORE anything
		// touches the filesystem, so a refusal cannot leave a half-built
		// worktree behind — the same ordering reason spawn.Launch validates its
		// templates pre-create.
		//
		// This sits in createWorktree rather than in Attach because Attach is
		// not the only way here: spawn.Launch calls Create directly, and a
		// worker dispatched into an untouched sibling repo is the single most
		// likely session to inherit a stale tree (spinclass#250). Gating at the
		// shared funnel closes that by construction instead of by remembering.
		//
		// Skipped when adopting an existing branch (start-gh_pr): that path's
		// base is the branch being adopted, by intent.
		base := ""
		if worktreePath.ExistingBranch == "" {
			res, ferr := basebranch.Freshen(
				context.Background(), worktreePath.RepoPath, opts.AllowStaleBase, true,
			)
			if ferr != nil {
				return false, ferr
			}
			reportBase(tw, worktreePath.RepoPath, res)
			base = res.BaseSha
		}

		result, err := worktree.Create(
			worktreePath.RepoPath, worktreePath.AbsPath, worktreePath.ExistingBranch, base,
		)
		if err != nil {
			return false, err
		}
		if opts.Verbose {
			logSweatfileResult(result)
		}
	}

	return existed, nil
}

// reportBase emits the base-branch step's test point. Successes and skips only:
// a failure aborts creation and travels back as an error, which the caller
// already surfaces, so a test point for it would report the same thing twice.
func reportBase(tw *tap.Writer, repoPath string, res basebranch.Result) {
	if tw == nil {
		return
	}
	desc := "base " + filepath.Base(repoPath)
	if res.Branch != "" {
		desc += " " + res.Branch
	}
	if res.Action.Skipped() {
		tw.Skip(desc, res.Reason)
		return
	}
	if res.Reason != "" {
		desc += " — " + res.Reason
	}
	tw.Ok(desc)
}

func logSweatfileResult(result sweatfile.Hierarchy) {
	for _, src := range result.Sources {
		if src.Found {
			log.Info("loaded sweatfile", "path", src.Path)
			if src.File.Git != nil && len(src.File.Git.Excludes) > 0 {
				log.Info("  git excludes", "values", src.File.Git.Excludes)
			}
			if src.File.Claude != nil && len(src.File.Claude.Allow) > 0 {
				log.Info("  claude allow", "values", src.File.Claude.Allow)
			}
		} else {
			log.Info("sweatfile not found (skipped)", "path", src.Path)
		}
	}
	merged := result.Merged
	var gitExcludes []string
	var claudeAllow []string
	if merged.Git != nil {
		gitExcludes = merged.Git.Excludes
	}
	if merged.Claude != nil {
		claudeAllow = merged.Claude.Allow
	}
	log.Info(
		"merged sweatfile",
		"git.excludes", gitExcludes,
		"claude.allow", claudeAllow,
	)
}

func Attach(w io.Writer, exec executor.Executor, rp worktree.ResolvedPath, sf sweatfile.Sweatfile, format string, mergeOnClose bool, noAttach bool, verbose bool, allowStaleBase bool) error {
	var tw *tap.Writer
	if format == "tap" {
		tw = tap.NewWriter(w)
	}

	// The CLI flag and the sweatfile knob are two spellings of one override, so
	// resolve them here and hand the rest of the call chain a single answer.
	allowStale := allowStaleBase || sf.AllowStaleBase()

	existed, err := Create(w, rp, CreateOpts{
		Verbose:        verbose,
		Format:         format,
		AllowStaleBase: allowStale,
	}, tw)
	if err != nil {
		return err
	}

	// Resuming an existing worktree still keeps the repo's default branch
	// current — this replaces the old pull step, which pulled whatever branch
	// the checkout happened to be on and so could advance a feature branch.
	// Advisory here: refusing to re-enter a session that already exists because
	// a remote is unreachable would be an obstruction, not a safeguard, so
	// Freshen is called in its non-required mode and cannot return an error.
	if existed {
		res, _ := basebranch.Freshen(context.Background(), rp.RepoPath, allowStale, false)
		reportBase(tw, rp.RepoPath, res)
	}

	tp := tap.TestPoint{
		Description: "attach " + rp.Branch,
		Ok:          true,
	}

	// Write session state. With --no-attach no entrypoint is exec'd, so the
	// session is recorded as inactive (PID 0) rather than active — but it IS
	// recorded, so `sc exec --session <key>`, `sc list`, and merge/close can
	// target it afterward (the create-then-operate workflow `sc run` builds on).
	{
		st := session.State{
			PID:          os.Getpid(),
			SessionState: session.StateActive,
			RepoPath:     rp.RepoPath,
			WorktreePath: rp.AbsPath,
			Branch:       rp.Branch,
			SessionKey:   rp.SessionKey,
			Description:  rp.Description,
			Env: map[string]string{
				"SPINCLASS_SESSION_ID": rp.SessionKey,
			},
		}
		if noAttach {
			st.PID = 0
			st.SessionState = session.StateInactive
		}
		if sexec, ok := exec.(executor.SessionExecutor); ok {
			st.Entrypoint = sexec.Entrypoint
		}
		// Attach owns the lifecycle fields (PID, state, entrypoint,
		// timestamps) but not these: FDR 0006 lineage and a buffered
		// pre-merge attestation must survive a resume (#147).
		if existing, err := session.Read(rp.RepoPath, rp.Branch); err == nil {
			st.SpawnedBy = existing.SpawnedBy
			st.HelloSentAt = existing.HelloSentAt
			st.PreMergeAttestation = existing.PreMergeAttestation
			// Carry the recorded setup fingerprint forward by default. A
			// resume (existed) did NOT re-apply setup (createWorktree only
			// applies on a fresh worktree), so a stale fingerprint must stay
			// stale until `sc rebuild` / resume auto-rebuild refreshes it.
			st.SetupFingerprint = existing.SetupFingerprint
			st.SetupScheme = existing.SetupScheme
			st.SetupAt = existing.SetupAt
		}
		if home, herr := os.UserHomeDir(); herr == nil {
			if !existed {
				// A freshly-created worktree just had its setup applied —
				// record the current fingerprint, overriding any carry-forward.
				if hash, scheme, ferr := worktree.SetupFingerprint(home, rp.RepoPath); ferr == nil {
					now := time.Now().UTC()
					st.SetupFingerprint, st.SetupScheme, st.SetupAt = hash, scheme, &now
				} else {
					log.Warn("failed to compute setup fingerprint", "err", ferr)
				}
			} else {
				// Resume of an existing worktree: setup was NOT re-applied
				// (createWorktree skips it). Warn if the recorded fingerprint
				// is stale, and — when [hooks].auto-rebuild-on-resume is set —
				// re-apply now and refresh the fingerprint before attaching.
				// Warnings go to stderr so they never corrupt TAP stdout.
				if stale, reason, serr := worktree.SetupStale(home, rp.RepoPath, st.SetupFingerprint, st.SetupScheme); serr == nil && stale {
					if sf.AutoRebuildOnResume() {
						if _, rerr := worktree.Reapply(rp.RepoPath, rp.AbsPath); rerr != nil {
							fmt.Fprintf(os.Stderr, "spinclass: auto-rebuild failed: %v\n", rerr)
						} else if hash, scheme, ferr := worktree.SetupFingerprint(home, rp.RepoPath); ferr == nil {
							now := time.Now().UTC()
							st.SetupFingerprint, st.SetupScheme, st.SetupAt = hash, scheme, &now
							fmt.Fprintln(os.Stderr, "spinclass: worktree setup was stale — auto-rebuilt")
						}
					} else {
						fmt.Fprintf(os.Stderr, "spinclass: worktree setup is stale (%s) — run `sc rebuild`\n", reason)
					}
				}
			}
		}
		st.StartedAt = time.Now().UTC()
		if err := session.Write(st); err != nil {
			log.Warn("failed to write session state", "err", err)
		}

		// Fire on-attach hook after state is committed but before
		// handing off to the entrypoint. Errors are warnings, not
		// fatal — a failing hook should never block session start.
		// Skipped for --no-attach: nothing is being attached, so the
		// "on attach" lifecycle moment never happens.
		if !noAttach {
			if hookErr := sf.RunOnAttachHook(rp.AbsPath, w); hookErr != nil {
				log.Warn("on-attach hook failed", "err", hookErr)
			}
		}
	}

	if err := exec.Attach(rp.AbsPath, rp.SessionKey, nil, noAttach, &tp); err != nil {
		return fmt.Errorf("attach failed: %w", err)
	}

	if noAttach {
		if tw != nil {
			tw.SkipDiag(tp.Description, tp.Skip, tp.Diagnostics)
			tw.Plan()
		}
		return nil
	}

	// Post-Attach state update. The spinclass-spawned entrypoint has
	// exited; consult clown's presence index to distinguish "harness still
	// alive, no attached spinclass client" (running-detached) from "process
	// truly gone" (inactive). Under the FDR-0017 posh cutover clown owns the
	// multiplexer attach and names sessions by its own per-instance key, so
	// the old `posh -g … list | grep $SPINCLASS_SESSION_ID` probe could never
	// match — presence (decoration == session key) is the liveness source now.
	// A missing/stale presence record degrades to inactive (never false-alive).
	now := time.Now().UTC()
	if existing, err := session.Read(rp.RepoPath, rp.Branch); err == nil {
		existing.PID = 0
		existing.ExitedAt = &now
		if len(clown.PresenceByDecoration(now)[existing.SessionKey]) > 0 {
			existing.SessionState = session.StateRunningDetached
		} else {
			existing.SessionState = session.StateInactive
		}
		if writeErr := session.Write(*existing); writeErr != nil {
			log.Warn("failed to update session state", "err", writeErr)
		}
	}

	// Fire on-detach hook AFTER state is committed so the hook can
	// observe the final state via $SPINCLASS_SESSION_ID + spinclass list.
	if hookErr := sf.RunOnDetachHook(rp.AbsPath, w); hookErr != nil {
		log.Warn("on-detach hook failed", "err", hookErr)
	}

	interactive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	return closeShop(w, exec, rp, format, mergeOnClose, tw, interactive, noAttach)
}

// runMergeOnClose runs one self-contained merge attempt with its own
// ndjson-crap renderer scope (merge/check no longer speak TAP — see
// docs/plans/2026-06-10-merge-ndjson-crap-viewport-design.md). The
// viewport/plain/ndjson selection mirrors `sc merge`: auto-resolve from
// stdout TTY-ness. huh prompts in closeShop stay outside this scope.
func runMergeOnClose(exec executor.Executor, rp worktree.ResolvedPath, defaultBranch string) error {
	resolved, err := present.ResolveFormat("", isatty.IsTerminal(os.Stdout.Fd()))
	if err != nil {
		return err
	}
	return present.WithReporter(resolved, "merge "+rp.Branch, os.Stdout, os.Stderr, func(rep *crap.Reporter) error {
		ts := rep.TestStream(0)
		defer ts.Finish()
		_, mergeErr := merge.Resolved(exec, rep, ts, rp.RepoPath, rp.AbsPath, rp.Branch, defaultBranch, false, false)
		return mergeErr
	})
}

func closeShop(w io.Writer, exec executor.Executor, rp worktree.ResolvedPath, format string, mergeOnClose bool, tw *tap.Writer, interactive bool, noAttach bool) error {
	if rp.Branch == "" {
		if err := rp.FillBranchFromGit(); err != nil {
			log.Warn("could not determine current branch")
			return nil
		}
	}

	defaultBranch, err := git.DefaultBranch(rp.RepoPath)
	if errors.Is(err, git.ErrAmbiguousDefaultBranch) {
		if interactive {
			defaultBranch, err = promptDefaultBranch()
			if err != nil {
				log.Warn("branch selection cancelled")
				return nil
			}
		} else {
			log.Warn("both main and master branches exist, skipping rebase")
			return nil
		}
	} else if err != nil || defaultBranch == "" {
		log.Warn("could not determine default branch")
		return nil
	}

	worktreeStatus := git.StatusPorcelain(rp.AbsPath)
	isClean := worktreeStatus == ""

	if isClean && mergeOnClose {
		if tw != nil {
			tw.Plan()
		}
		return runMergeOnClose(exec, rp, defaultBranch)
	}

	if interactive && mergeOnClose {
		for {
			action, promptErr := promptDirtyAction(rp.Branch)
			if promptErr != nil {
				break
			}

			switch action {
			case actionDiscard:
				if discardErr := discardAll(rp.AbsPath); discardErr != nil {
					if tw != nil {
						tw.NotOk("discard "+rp.Branch, map[string]string{
							"severity": "fail",
							"message":  discardErr.Error(),
						})
						tw.Plan()
					}
					return discardErr
				}
				if tw != nil {
					tw.Plan()
				}
				return runMergeOnClose(exec, rp, defaultBranch)

			case actionReattach:
				tp := tap.TestPoint{
					Description: "reattach " + rp.Branch,
					Ok:          true,
				}
				if attachErr := exec.Attach(rp.AbsPath, rp.SessionKey, nil, noAttach, &tp); attachErr != nil {
					return fmt.Errorf("reattach failed: %w", attachErr)
				}
				worktreeStatus = git.StatusPorcelain(rp.AbsPath)
				isClean = worktreeStatus == ""
				if isClean {
					if tw != nil {
						tw.Plan()
					}
					return runMergeOnClose(exec, rp, defaultBranch)
				}
				continue

			case actionExit:
			}
			break
		}
	}

	commitsAhead := git.CommitsAhead(rp.AbsPath, defaultBranch, rp.Branch)
	desc := statusDescription(defaultBranch, commitsAhead, worktreeStatus)

	if tw != nil {
		tw.Ok("close " + rp.Branch + " # " + desc)
		tw.Plan()
	} else if format == "tap" {
		tw = tap.NewWriter(w)
		tw.Ok("close " + rp.Branch + " # " + desc)
		tw.Plan()
	} else {
		log.Info(desc, "worktree", rp.SessionKey)
	}

	// Auto-close prompt: when the branch is fully merged into the
	// default and the worktree is clean, offer to tear down the
	// session before returning. Gated on interactive (TTY) for the
	// huh path so headless callers (CI, --no-attach, no TTY) never
	// hang on the prompt. Headless callers can still drive the path
	// by setting SPINCLASS_AUTOCLOSE_ASSUME=yes|no, which bypasses
	// the prompt and uses the env value directly — primarily for
	// bats coverage of the merged-and-clean branch. See #66.
	if commitsAhead == 0 && worktreeStatus == "" {
		if assume, set := parseAutocloseAssume(); set {
			if assume {
				return close.RunResolved(w, rp.RepoPath, rp.AbsPath, rp.Branch, false, nil, format)
			}
		} else if interactive {
			statusOut, _ := git.Run(rp.AbsPath, "status")
			proceed, perr := promptCloseFullyMerged(rp.Branch, defaultBranch, statusOut)
			if perr == nil && proceed {
				return close.RunResolved(w, rp.RepoPath, rp.AbsPath, rp.Branch, false, nil, format)
			}
		}
	}

	return nil
}

func discardAll(wtPath string) error {
	if _, err := git.Run(wtPath, "checkout", "."); err != nil {
		return fmt.Errorf("git checkout: %w", err)
	}
	if _, err := git.Run(wtPath, "clean", "-fd"); err != nil {
		return fmt.Errorf("git clean: %w", err)
	}
	return nil
}

func statusDescription(defaultBranch string, commitsAhead int, porcelain string) string {
	var parts []string

	if commitsAhead == 1 {
		parts = append(parts, fmt.Sprintf("1 commit ahead of %s", defaultBranch))
	} else {
		parts = append(parts, fmt.Sprintf("%d commits ahead of %s", commitsAhead, defaultBranch))
	}

	if porcelain == "" {
		parts = append(parts, "clean")
	} else {
		parts = append(parts, "dirty")
	}

	if commitsAhead == 0 && porcelain == "" {
		parts = append(parts, "(merged)")
	}

	return strings.Join(parts, ", ")
}

// Fork creates a new worktree branched from rp's current HEAD.
// If newBranch is empty, a name is auto-generated as <rp.Branch>-N.
// Does not attach to the new session.
func Fork(
	writer io.Writer,
	worktreePath worktree.ResolvedPath,
	newBranch string,
	format string,
) error {
	if newBranch == "" {
		newBranch = worktree.ForkName(worktreePath.RepoPath, worktreePath.Branch)
	}

	newPath := filepath.Join(worktreePath.RepoPath, worktree.WorktreesDir, newBranch)

	if _, err := worktree.CreateFrom(
		worktreePath.RepoPath,
		worktreePath.AbsPath,
		newPath,
		newBranch,
	); err != nil {
		return err
	}

	if format == "tap" {
		tw := tap.NewWriter(writer)
		tw.PlanAhead(1)
		tw.Ok("fork " + newBranch + " " + newPath)
		return nil
	}

	_, _ = fmt.Fprintln(writer, newPath)
	return nil
}
