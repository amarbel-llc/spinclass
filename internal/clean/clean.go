package clean

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/mattn/go-isatty"

	"code.linenisgreat.com/spinclass/internal/auth"
	"code.linenisgreat.com/spinclass/internal/check"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/nixgc"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/tapblock"
	"code.linenisgreat.com/spinclass/internal/worktree"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
	"code.linenisgreat.com/tap/go/pkgs/yaml_diagnostic"
)

var styleCode = lipgloss.NewStyle().Foreground(lipgloss.Color("#E88388")).Background(lipgloss.Color("#1D1F21")).Padding(0, 1)

// cleanInteractive reports whether both stdin and stderr are TTYs. huh renders
// via stderr (tea.WithOutput(os.Stderr)), so both fds must be terminals before
// we invoke any interactive prompt. Overridable in tests.
var cleanInteractive = func() bool {
	stdin := os.Stdin.Fd()
	stderr := os.Stderr.Fd()
	return (isatty.IsTerminal(stdin) || isatty.IsCygwinTerminal(stdin)) &&
		(isatty.IsTerminal(stderr) || isatty.IsCygwinTerminal(stderr))
}

type FileChange struct {
	Code string
	Path string
}

func ParsePorcelain(porcelain string) []FileChange {
	var changes []FileChange
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		path := line[3:]
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		changes = append(changes, FileChange{Code: code, Path: path})
	}
	return changes
}

func (fc FileChange) Description() string {
	switch {
	case fc.Code == "??":
		return "untracked"
	case fc.Code[1] == 'D' || fc.Code[0] == 'D':
		return "deleted"
	case fc.Code[0] == 'A':
		return "added"
	case fc.Code[0] == 'R':
		return "renamed"
	default:
		return "modified"
	}
}

type worktreeInfo struct {
	repo         string
	branch       string
	repoPath     string
	worktreePath string
	merged       bool
	dirty        bool
}

func scanWorktrees(startDir string) []worktreeInfo {
	var worktrees []worktreeInfo

	repos := worktree.ScanRepos(startDir)
	for _, repoPath := range repos {
		repoName := filepath.Base(repoPath)

		defaultBranch, err := git.DefaultBranch(repoPath)
		if err != nil || defaultBranch == "" {
			continue
		}

		for _, wtPath := range worktree.ListWorktrees(repoPath) {
			branch := filepath.Base(wtPath)

			ahead := git.CommitsUnintegrated(wtPath, defaultBranch, branch)
			porcelain := git.StatusPorcelain(wtPath)

			worktrees = append(worktrees, worktreeInfo{
				repo:         repoName,
				branch:       branch,
				repoPath:     repoPath,
				worktreePath: wtPath,
				merged:       ahead == 0,
				dirty:        porcelain != "",
			})
		}
	}

	return worktrees
}

func removeWorktree(wt worktreeInfo, tw *tap.Writer) error {
	// Capture nix gc roots BEFORE worktree removal so the auto-roots' link
	// targets still resolve. Reap runs AFTER the worktree (and its `result`
	// symlinks) is gone, so nix's own liveness check decides which closure
	// paths actually disappear.
	//
	// Do NOT flip this order. `nix-store --gc --print-roots` silently
	// omits any indirect root whose target file is gone — moving the plan
	// step to post-removal would lose every store path the worktree was
	// holding alive. See close.go's RunResolved for the same invariant
	// and issue #67 for the empirical reproduction.
	gcPlan := planNixGCForClean(wt.repoPath, wt.worktreePath)

	// Revoke the session's forge push credential (FDR 0028) before the
	// worktree goes — closing the gap where a merged worktree reaped here
	// would orphan its token. Best-effort; the orphan sweep is the backstop.
	revokeCredential(tw, wt)

	if err := git.WorktreeRemove(wt.repoPath, wt.worktreePath); err != nil {
		return fmt.Errorf("removing worktree %s: %w", wt.branch, err)
	}
	if _, err := git.BranchDelete(wt.repoPath, wt.branch); err != nil {
		return fmt.Errorf("deleting branch %s: %w", wt.branch, err)
	}
	// Clean up session state file if it exists (best-effort)
	_ = session.Remove(wt.repoPath, wt.branch)

	if gcPlan != nil {
		runReap(tw, *gcPlan, wt.branch)
	}
	return nil
}

// revokeCredential runs [auth].revoke-command for a worktree that minted a
// credential, as one TAP test point; mirrors close.revokeCredential.
func revokeCredential(tw *tap.Writer, wt worktreeInfo) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	h, err := sweatfileio.LoadWorktreeHierarchy(home, wt.repoPath, wt.worktreePath)
	if err != nil {
		return
	}
	sessionKey := filepath.Base(wt.repoPath) + "/" + wt.branch
	if st, rerr := session.Read(wt.repoPath, wt.branch); rerr == nil && st.SessionKey != "" {
		sessionKey = st.SessionKey
	}
	var out bytes.Buffer
	id := auth.Identity{RepoPath: wt.repoPath, WorktreePath: wt.worktreePath, Branch: wt.branch, SessionKey: sessionKey}
	ran, rerr := auth.Revoke(context.Background(), h.Merged, id, &out)
	if !ran {
		return
	}
	desc := "revoke credential " + wt.branch
	if rerr != nil {
		diag := map[string]string{"severity": "warn", "message": rerr.Error()}
		if o := strings.TrimSpace(out.String()); o != "" {
			diag["output"] = o
		}
		if tw != nil {
			tw.NotOk(desc, diag)
		} else {
			log.Warn("credential revoke failed", "branch", wt.branch, "err", rerr)
		}
		return
	}
	if tw != nil {
		tw.Ok(desc)
	}
}

// runReap executes nixgc.Reap as a single TAP test point with live
// stdout/stderr streaming into the OutputBlock so the operator can
// see per-path progress (and notice if a path stalls). Mirrors
// internal/close/close.go runReap; duplicated rather than shared so
// each command's output handling stays self-contained.
func runReap(tw *tap.Writer, plan nixgc.Plan, branch string) {
	desc := fmt.Sprintf("nix-gc reap %s", branch)
	if tw == nil {
		nixgc.Reap(plan, os.Stderr, os.Stderr)
		return
	}
	var summary nixgc.Summary
	tw.OutputBlock(desc, func(ob *tap.OutputBlockWriter) *yaml_diagnostic.YAMLDiagnostic {
		lw := tapblock.NewLineWriter(ob)
		summary = nixgc.Reap(plan, lw, lw)
		lw.Flush()
		extras := map[string]any{
			"reclaimed":         summary.Reclaimed,
			"kept":              summary.Kept,
			"timed_out":         summary.TimedOut,
			"closure":           len(plan.Closure),
			"bytes_freed":       summary.BytesFreed,
			"bytes_freed_human": summary.HumanFreed(),
			"bytes_kept":        summary.BytesKept,
			"bytes_kept_human":  summary.HumanKept(),
		}
		if summary.TimedOut > 0 {
			return &yaml_diagnostic.YAMLDiagnostic{
				Severity: "warn",
				Message: fmt.Sprintf(
					"%d path(s) still in flight when nix-store --delete timed out (see #68)",
					summary.TimedOut,
				),
				Extras: extras,
			}
		}
		if len(summary.Errors) > 0 {
			extras["errors"] = len(summary.Errors)
			extras["first_error"] = summary.Errors[0].Error()
			return &yaml_diagnostic.YAMLDiagnostic{
				Severity: "warn",
				Message:  fmt.Sprintf("%d path(s) failed to delete", len(summary.Errors)),
				Extras:   extras,
			}
		}
		// Success: emit stats as body lines and return nil so the
		// OutputBlock renders "ok". Non-nil return — even one carrying
		// only Extras — is the library's "not ok" signal.
		writeReapSummary(ob, extras)
		return nil
	})
}

// writeReapSummary emits the reap stats as body lines inside the
// OutputBlock. Used on the success path where the callback must return
// nil (to render "ok") but we still want the structured summary visible
// to operators reading the TAP stream. Keys are emitted in a fixed
// human-friendly order rather than sorted alphabetically.
func writeReapSummary(ob *tap.OutputBlockWriter, extras map[string]any) {
	keys := []string{
		"reclaimed",
		"kept",
		"timed_out",
		"closure",
		"bytes_freed",
		"bytes_freed_human",
		"bytes_kept",
		"bytes_kept_human",
	}
	for _, k := range keys {
		v, ok := extras[k]
		if !ok {
			continue
		}
		ob.Line(fmt.Sprintf("%s: %v", k, v))
	}
}

// planNixGCForClean is the override-less twin of close.planNixGC. Returns
// nil when the gc should not run (sweatfile disabled, nix missing, no roots,
// or any plan-build error).
func planNixGCForClean(repoPath, wtPath string) *nixgc.Plan {
	if nixgc.Disabled(repoPath, wtPath) {
		return nil
	}
	plan, err := nixgc.NewPlan(wtPath)
	if errors.Is(err, nixgc.ErrNixUnavailable) {
		return nil
	}
	if errors.Is(err, nixgc.ErrPlanTimedOut) {
		// nix-store stalled past planTimeout. Skip this worktree's gc;
		// a later sc clean retry will pick it up.
		return nil
	}
	if err != nil {
		return nil
	}
	if len(plan.Roots) == 0 {
		return nil
	}
	return &plan
}

func discardFile(wtPath string, fc FileChange) error {
	if fc.Code == "??" {
		return os.Remove(filepath.Join(wtPath, fc.Path))
	}
	if fc.Code[0] != ' ' {
		if err := git.ResetFile(wtPath, fc.Path); err != nil {
			return err
		}
	}
	return git.CheckoutFile(wtPath, fc.Path)
}

func handleDirtyWorktree(wt worktreeInfo, tw *tap.Writer) (removed bool, err error) {
	porcelain := git.StatusPorcelain(wt.worktreePath)
	changes := ParsePorcelain(porcelain)

	if !cleanInteractive() {
		return false, fmt.Errorf("sc clean -i requires an interactive terminal to review dirty files; omit -i to skip dirty worktrees")
	}

	for _, fc := range changes {
		var discard bool
		prompt := fmt.Sprintf("Discard %s (%s)?", fc.Path, fc.Description())
		err := huh.NewConfirm().
			Title(prompt).
			Value(&discard).
			Run()
		if err != nil {
			return false, err
		}
		if discard {
			if err := discardFile(wt.worktreePath, fc); err != nil {
				log.Warn("failed to discard file", "file", fc.Path, "err", err)
			}
		}
	}

	recheckPorcelain := git.StatusPorcelain(wt.worktreePath)
	if recheckPorcelain != "" {
		return false, nil
	}

	if err := removeWorktree(wt, tw); err != nil {
		return false, err
	}
	return true, nil
}

func countAbandonedSessions() (int, []session.State) {
	states, err := session.ListAll(nil)
	if err != nil {
		return 0, nil
	}
	var abandoned []session.State
	for _, s := range states {
		if s.ResolveState() == session.StateAbandoned {
			abandoned = append(abandoned, s)
		}
	}
	return len(abandoned), abandoned
}

func removeAbandonedSessions(abandoned []session.State) int {
	removed := 0
	for _, s := range abandoned {
		_ = session.Remove(s.RepoPath, s.Branch)
		removed++
	}
	return removed
}

// orphanTransientWorktree is a leftover transient merge worktree whose creating
// process is no longer alive: either a pre-merge build worktree
// (.merge-<branch>-<sha>-<pid>, check.resolveHookDir — see issue #135) or a
// merge-queue landing worktree (.land-<branch>-<shortsha>-<pid>,
// merge.rebaseLanding, FDR 0022 — see issue #237), both under
// <repo>/.worktrees/. Normally removed by their deferred cleanup, but a
// crashed merge leaves one behind.
type orphanTransientWorktree struct {
	repoPath string
	path     string // absolute path to the .merge-* / .land-* dir
	name     string // basename, for display
}

// findOrphanTransientWorktrees scans repos under startDir for .merge-* build and
// .land-* landing worktree dirs whose embedded <pid> is dead. A LIVE-pid dir
// (an in-flight merge) is left untouched. The PID is the segment after the
// last '-' in the name (branch/sha segments may themselves contain '-', so
// parse from the end).
func findOrphanTransientWorktrees(startDir string) []orphanTransientWorktree {
	var orphans []orphanTransientWorktree
	for _, repoPath := range worktree.ScanRepos(startDir) {
		wtDir := filepath.Join(repoPath, worktree.WorktreesDir)
		entries, err := os.ReadDir(wtDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !isTransientMergeWorktreeName(e.Name()) {
				continue
			}
			pid, ok := pidFromTransientWorktreeName(e.Name())
			if !ok {
				continue // unparseable name — leave it alone, don't guess
			}
			if session.IsAlive(pid) {
				continue // in-flight merge — never touch a live one
			}
			orphans = append(orphans, orphanTransientWorktree{
				repoPath: repoPath,
				path:     filepath.Join(wtDir, e.Name()),
				name:     e.Name(),
			})
		}
	}
	return orphans
}

// isTransientMergeWorktreeName reports whether name is a transient merge
// worktree: a .merge-* pre-merge build worktree or a .land-* merge-queue
// landing worktree.
func isTransientMergeWorktreeName(name string) bool {
	return strings.HasPrefix(name, check.BuildWorktreePrefix) ||
		strings.HasPrefix(name, merge.LandWorktreePrefix)
}

// pidFromTransientWorktreeName extracts the trailing <pid> from a
// .merge-<branch>-<sha>-<pid> or .land-<branch>-<shortsha>-<pid> name. Returns
// (0,false) if the last '-'-segment isn't a positive integer. Branch/sha
// segments can contain '-', so only the final segment is the PID. The name
// formats are produced by check.resolveHookDir (prefix
// check.BuildWorktreePrefix) and merge.rebaseLanding (prefix
// merge.LandWorktreePrefix); keep this parser in sync with them.
func pidFromTransientWorktreeName(name string) (int, bool) {
	i := strings.LastIndex(name, "-")
	if i < 0 || i == len(name)-1 {
		return 0, false
	}
	pid, err := strconv.Atoi(name[i+1:])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// removeOrphanTransientWorktree force-removes one orphaned transient worktree and
// prunes the repo's stale admin entries. Best-effort: force-remove the git
// worktree (clears the registration if any), then RemoveAll the physical dir
// in case it was unregistered (the crash-orphan case), then prune.
func removeOrphanTransientWorktree(o orphanTransientWorktree) error {
	// ForceRemove clears the git admin entry (and the dir, if it was a
	// registered worktree); ignored because a crash-orphan is typically
	// unregistered. RemoveAll then guarantees the physical dir is gone — a
	// no-op if ForceRemove already deleted it — and its error IS propagated.
	// Prune clears any leftover stale admin entries; best-effort.
	_ = git.WorktreeForceRemove(o.repoPath, o.path)
	if err := os.RemoveAll(o.path); err != nil {
		return err
	}
	_ = git.WorktreePrune(o.repoPath)
	return nil
}

// countStaleTombstones returns how many tombstone files at the central
// index path have an `exited_at` older than `cutoff`. retention <= 0
// disables GC and returns 0. Walks via session.ListAll which already
// classifies entries (live vs tombstone vs dangling).
func countStaleTombstones(retention time.Duration) int {
	if retention <= 0 {
		return 0
	}
	states, err := session.ListAll(nil)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-retention)
	count := 0
	for _, s := range states {
		if !s.IsTombstone() {
			continue
		}
		if s.ExitedAt == nil || s.ExitedAt.After(cutoff) {
			continue
		}
		count++
	}
	return count
}

// resolveTombstoneRetention loads the merged sweatfile from PWD and
// returns the configured retention, falling back to the package
// default if unset or unparseable.
func resolveTombstoneRetention(startDir string) time.Duration {
	home, err := os.UserHomeDir()
	if err != nil {
		return session.DefaultTombstoneRetention()
	}
	hierarchy, err := sweatfileio.LoadHierarchy(home, startDir)
	if err != nil {
		return session.DefaultTombstoneRetention()
	}
	if d, ok := hierarchy.Merged.TombstoneRetention(); ok {
		return d
	}
	return session.DefaultTombstoneRetention()
}

type cleanAction struct {
	wt     worktreeInfo
	label  string
	action string // "remove", "skip-dirty", "interactive"
}

func planClean(worktrees []worktreeInfo, interactive bool) []cleanAction {
	var actions []cleanAction
	for _, wt := range worktrees {
		if !wt.merged {
			continue
		}
		label := filepath.Join(wt.repo, worktree.WorktreesDir) + "/" + styleCode.Render(wt.branch)
		if !wt.dirty {
			actions = append(actions, cleanAction{wt: wt, label: label, action: "remove"})
		} else if interactive {
			actions = append(actions, cleanAction{wt: wt, label: label, action: "interactive"})
		} else {
			actions = append(actions, cleanAction{wt: wt, label: label, action: "skip-dirty"})
		}
	}
	return actions
}

func emitPlan(tw *tap.Writer, actions []cleanAction, abandonedCount int, tombstoneCount int, orphanBuildCount int, dryRun bool) {
	reason := "dry-run"
	for _, a := range actions {
		switch a.action {
		case "remove":
			if dryRun {
				if tw != nil {
					tw.Skip("remove "+a.label, reason)
				} else {
					log.Info("would remove", "worktree", a.label)
				}
			} else {
				if tw != nil {
					tw.Skip("remove "+a.label, "pending confirmation")
				} else {
					log.Info("will remove", "worktree", a.label)
				}
			}
		case "interactive":
			if tw != nil {
				tw.Skip("remove "+a.label, "dirty, will prompt")
			} else {
				log.Info("dirty, will prompt", "worktree", a.label)
			}
		case "skip-dirty":
			if tw != nil {
				tw.Skip("remove "+a.label, "dirty worktree")
			} else {
				log.Warn("skipping dirty worktree", "worktree", a.label)
			}
		}
	}
	if abandonedCount > 0 {
		msg := fmt.Sprintf("clean %d abandoned session(s)", abandonedCount)
		if dryRun {
			if tw != nil {
				tw.Skip(msg, reason)
			} else {
				log.Info("would " + msg)
			}
		} else {
			if tw != nil {
				tw.Skip(msg, "pending confirmation")
			} else {
				log.Info("will " + msg)
			}
		}
	}
	if tombstoneCount > 0 {
		msg := fmt.Sprintf("GC %d stale tombstone(s)", tombstoneCount)
		if dryRun {
			if tw != nil {
				tw.Skip(msg, reason)
			} else {
				log.Info("would " + msg)
			}
		} else {
			if tw != nil {
				tw.Skip(msg, "pending confirmation")
			} else {
				log.Info("will " + msg)
			}
		}
	}
	if orphanBuildCount > 0 {
		msg := fmt.Sprintf("prune %d orphaned transient worktree(s)", orphanBuildCount)
		if dryRun {
			if tw != nil {
				tw.Skip(msg, reason)
			} else {
				log.Info("would " + msg)
			}
		} else {
			if tw != nil {
				tw.Skip(msg, "pending confirmation")
			} else {
				log.Info("will " + msg)
			}
		}
	}
}

func confirmClean(removeCount, abandonedCount, tombstoneCount, orphanBuildCount int) (bool, error) {
	if !cleanInteractive() {
		return false, fmt.Errorf("sc clean requires an interactive terminal to confirm; use --yes to skip (and omit -i to skip dirty-file review)")
	}
	parts := []string{}
	if removeCount > 0 {
		parts = append(parts, fmt.Sprintf("%d worktree(s)", removeCount))
	}
	if abandonedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d abandoned session(s)", abandonedCount))
	}
	if tombstoneCount > 0 {
		parts = append(parts, fmt.Sprintf("%d stale tombstone(s)", tombstoneCount))
	}
	if orphanBuildCount > 0 {
		parts = append(parts, fmt.Sprintf("%d orphaned transient worktree(s)", orphanBuildCount))
	}
	prompt := fmt.Sprintf("Remove %s?", strings.Join(parts, " and "))
	var confirmed bool
	err := huh.NewConfirm().
		Title(prompt).
		Value(&confirmed).
		Run()
	if err != nil {
		return false, err
	}
	return confirmed, nil
}

func executeClean(tw *tap.Writer, actions []cleanAction, abandoned []session.State, orphans []orphanTransientWorktree, retention time.Duration, interactive bool) {
	for _, a := range actions {
		switch a.action {
		case "remove":
			if err := removeWorktree(a.wt, tw); err != nil {
				if tw != nil {
					tw.NotOk("remove "+a.label, map[string]string{
						"error": err.Error(),
					})
				} else {
					log.Error("failed to remove worktree", "branch", a.wt.branch, "error", err)
				}
				continue
			}
			if tw != nil {
				tw.Ok("remove " + a.label)
			} else {
				log.Info("removed merged worktree", "branch", a.wt.branch)
			}
		case "interactive":
			wasRemoved, err := handleDirtyWorktree(a.wt, tw)
			if err != nil {
				if tw != nil {
					tw.NotOk("remove "+a.label, map[string]string{
						"error": err.Error(),
					})
				} else {
					log.Error("failed to remove worktree", "branch", a.wt.branch, "error", err)
				}
				continue
			}
			if wasRemoved {
				if tw != nil {
					tw.Ok("remove " + a.label)
				} else {
					log.Info("removed merged worktree", "branch", a.wt.branch)
				}
			} else {
				if tw != nil {
					tw.Skip("remove "+a.label, "kept after interactive review")
				} else {
					log.Info("kept worktree after interactive review", "branch", a.wt.branch)
				}
			}
		case "skip-dirty":
			if tw != nil {
				tw.Skip("remove "+a.label, "dirty worktree")
			} else {
				log.Warn("skipping dirty worktree", "branch", a.wt.branch)
			}
		}
	}

	if len(abandoned) > 0 {
		removed := removeAbandonedSessions(abandoned)
		if tw != nil {
			tw.Ok(fmt.Sprintf("cleaned %d abandoned session(s)", removed))
		}
	}

	if len(orphans) > 0 {
		pruned := 0
		for _, o := range orphans {
			if err := removeOrphanTransientWorktree(o); err != nil {
				if tw != nil {
					tw.NotOk("prune orphaned transient worktree "+o.name, map[string]string{
						"error": err.Error(),
					})
				} else {
					log.Error("failed to prune orphaned transient worktree", "name", o.name, "error", err)
				}
				continue
			}
			pruned++
		}
		if pruned > 0 {
			if tw != nil {
				tw.Ok(fmt.Sprintf("pruned %d orphaned transient worktree(s)", pruned))
			} else {
				log.Info("pruned orphaned transient worktrees", "count", pruned)
			}
		}
	}

	if retention > 0 {
		gcCount, err := session.GCTombstones(retention)
		if err != nil {
			if tw != nil {
				tw.NotOk("GC tombstones", map[string]string{"error": err.Error()})
			} else {
				log.Error("failed to GC tombstones", "error", err)
			}
		} else if gcCount > 0 {
			if tw != nil {
				tw.Ok(fmt.Sprintf("GC'd %d stale tombstone(s)", gcCount))
			} else {
				log.Info("GC'd stale tombstones", "count", gcCount)
			}
		}
	}
}

func Run(startDir string, interactive bool, dryRun bool, yes bool, format string) error {
	var tw *tap.Writer
	if format == "tap" {
		tw = tap.NewWriter(os.Stdout)
	}

	worktrees := scanWorktrees(startDir)
	abandonedCount, abandonedSessions := countAbandonedSessions()
	retention := resolveTombstoneRetention(startDir)
	tombstoneCount := countStaleTombstones(retention)
	orphans := findOrphanTransientWorktrees(startDir)
	orphanCount := len(orphans)

	if len(worktrees) == 0 && abandonedCount == 0 && tombstoneCount == 0 && orphanCount == 0 {
		if tw != nil {
			tw.Skip("clean", "no worktrees found")
			tw.Plan()
		} else {
			log.Info("no worktrees found")
		}
		return nil
	}

	actions := planClean(worktrees, interactive)

	// Count how many worktrees will actually be removed (not skipped).
	removeCount := 0
	for _, a := range actions {
		if a.action == "remove" || a.action == "interactive" {
			removeCount++
		}
	}

	// Nothing actionable — just report skips and return.
	if removeCount == 0 && abandonedCount == 0 && tombstoneCount == 0 && orphanCount == 0 {
		emitPlan(tw, actions, abandonedCount, tombstoneCount, orphanCount, dryRun)
		if tw != nil {
			tw.Plan()
		}
		return nil
	}

	// Show what will happen.
	emitPlan(tw, actions, abandonedCount, tombstoneCount, orphanCount, dryRun)

	if dryRun {
		if tw != nil {
			tw.Plan()
		}
		return nil
	}

	// Confirm unless --yes.
	if !yes {
		confirmed, err := confirmClean(removeCount, abandonedCount, tombstoneCount, orphanCount)
		if err != nil {
			if tw != nil {
				tw.Plan()
			}
			return err
		}
		if !confirmed {
			if tw != nil {
				tw.Skip("clean", "cancelled by user")
				tw.Plan()
			} else {
				log.Info("clean cancelled")
			}
			return nil
		}
	}

	executeClean(tw, actions, abandonedSessions, orphans, retention, interactive)

	if tw != nil {
		tw.Plan()
	}
	return nil
}
