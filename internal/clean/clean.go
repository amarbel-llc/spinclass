package clean

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/nixgc"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

var styleCode = lipgloss.NewStyle().Foreground(lipgloss.Color("#E88388")).Background(lipgloss.Color("#1D1F21")).Padding(0, 1)

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

			ahead := git.CommitsAhead(wtPath, defaultBranch, branch)
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
	gcPlan := planNixGCForClean(wt.repoPath, wt.worktreePath)

	if err := git.WorktreeRemove(wt.repoPath, wt.worktreePath); err != nil {
		return fmt.Errorf("removing worktree %s: %w", wt.branch, err)
	}
	if _, err := git.BranchDelete(wt.repoPath, wt.branch); err != nil {
		return fmt.Errorf("deleting branch %s: %w", wt.branch, err)
	}
	// Clean up session state file if it exists
	session.Remove(wt.repoPath, wt.branch)

	if gcPlan != nil {
		summary := nixgc.Reap(*gcPlan)
		emitNixGCSummary(tw, summary)
	}
	return nil
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
	if err != nil {
		return nil
	}
	if len(plan.Roots) == 0 {
		return nil
	}
	return &plan
}

// emitNixGCSummary writes one TAP line per cleaned worktree summarizing the
// reap outcome. Errors surface as `not ok` but do NOT propagate; the
// worktree is already gone, so gc failure is informational.
func emitNixGCSummary(tw *tap.Writer, s nixgc.Summary) {
	if tw == nil {
		return
	}
	desc := fmt.Sprintf(
		"nix-gc: reclaimed %d path(s), kept %d (still rooted)",
		s.Reclaimed, s.Kept,
	)
	if len(s.Errors) == 0 {
		tw.Ok(desc)
		return
	}
	diag := map[string]string{
		"errors": fmt.Sprintf("%d", len(s.Errors)),
		"first":  s.Errors[0].Error(),
	}
	tw.NotOk(desc, diag)
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
		session.Remove(s.RepoPath, s.Branch)
		removed++
	}
	return removed
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
	hierarchy, err := sweatfile.LoadHierarchy(home, startDir)
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

func emitPlan(tw *tap.Writer, actions []cleanAction, abandonedCount int, tombstoneCount int, dryRun bool) {
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
}

func confirmClean(removeCount, abandonedCount, tombstoneCount int) (bool, error) {
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

func executeClean(tw *tap.Writer, actions []cleanAction, abandoned []session.State, retention time.Duration, interactive bool) {
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

	if len(worktrees) == 0 && abandonedCount == 0 && tombstoneCount == 0 {
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
	if removeCount == 0 && abandonedCount == 0 && tombstoneCount == 0 {
		emitPlan(tw, actions, abandonedCount, tombstoneCount, dryRun)
		if tw != nil {
			tw.Plan()
		}
		return nil
	}

	// Show what will happen.
	emitPlan(tw, actions, abandonedCount, tombstoneCount, dryRun)

	if dryRun {
		if tw != nil {
			tw.Plan()
		}
		return nil
	}

	// Confirm unless --yes.
	if !yes {
		confirmed, err := confirmClean(removeCount, abandonedCount, tombstoneCount)
		if err != nil {
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

	executeClean(tw, actions, abandonedSessions, retention, interactive)

	if tw != nil {
		tw.Plan()
	}
	return nil
}
