package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sessionpick"
)

// resumeConfirmKind names which confirmation dialog (if any) resume
// shows before attaching. See
// docs/plans/2026-06-06-clown-style-resume-design.md decision 2.
type resumeConfirmKind int

const (
	resumeConfirmNone resumeConfirmKind = iota
	resumeConfirmStandard
	resumeConfirmWarning
)

// resumeConfirmPlan is the pure decision seam for the resume
// confirmation flow.
//
// explicitTarget covers every path where the user already affirmed the
// target: naming an id on the command line ("naming is the
// confirmation" — this also keeps remote attach templates that exec
// `spinclass resume <id>` non-interactive) and selecting from the
// multi-match picker (selection is the confirmation, clown parity).
// Those paths never see a dialog, even for active sessions, so scripts
// keep working.
//
// The remaining paths — auto-detect from cwd and the picker's
// single-match short-circuit — show the standard confirm unless -y, or
// the warning variant (default Cancel) when the session resolves
// active, i.e. a live PID that is probably attached elsewhere.
//
// Divergence from clown documented per the design doc: clown's warning
// variant lives in its named-target flow and has no skip flag; here -y
// deliberately does NOT skip the warning variant either — skipping a
// default-Cancel warning with a convenience flag would defeat its
// purpose.
func resumeConfirmPlan(resolvedState string, explicitTarget, yes, isTTY bool) (resumeConfirmKind, error) {
	if explicitTarget {
		return resumeConfirmNone, nil
	}
	if resolvedState == session.StateActive {
		if !isTTY {
			return resumeConfirmNone, errors.New(
				"session is active (probably attached elsewhere); resume requires an interactive terminal to confirm",
			)
		}
		return resumeConfirmWarning, nil
	}
	if yes {
		return resumeConfirmNone, nil
	}
	if !isTTY {
		return resumeConfirmNone, errors.New(
			"resume requires an interactive terminal (or pass -y to skip confirmation)",
		)
	}
	return resumeConfirmStandard, nil
}

// resumeConfirmTitle mirrors the picker row title: the session
// description, falling back to the worktree dir name.
func resumeConfirmTitle(s *session.State) string {
	if s.Description != "" {
		return s.Description
	}
	return filepath.Base(s.WorktreePath)
}

// buildResumeDetail renders the indented key: value detail block shown
// under the confirm title, mirroring clown's buildResumeDesc. The
// warning variant prepends a warning paragraph. resolvedState is passed
// in (rather than re-resolved) so the block matches what the plan
// decision saw and the builder stays pure for tests.
func buildResumeDetail(s *session.State, resolvedState string, warnActive bool, now time.Time) string {
	var b strings.Builder
	if warnActive {
		fmt.Fprintln(&b, "Warning: this session is active — it is probably attached elsewhere.")
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "  id:        %s\n", filepath.Base(s.WorktreePath))
	fmt.Fprintf(&b, "  repo:      %s\n", s.SessionKey)
	fmt.Fprintf(&b, "  state:     %s\n", resolvedState)
	fmt.Fprintf(&b, "  last:      %s\n", sessionpick.FormatRelDate(sessionpick.LastActivity(*s), now))
	fmt.Fprintf(&b, "  worktree:  %s\n", s.WorktreePath)
	return b.String()
}

// confirmResume renders the resume confirmation dialog for kind
// (standard defaults to Resume; warning defaults to Cancel). Returns
// (false, nil) when the user cancels or dismisses. Thin huh glue,
// untested like clown's.
func confirmResume(s *session.State, resolvedState string, kind resumeConfirmKind) (bool, error) {
	warn := kind == resumeConfirmWarning
	title := fmt.Sprintf("Resume %q?", resumeConfirmTitle(s))
	if warn {
		title = fmt.Sprintf("Resume %q while it is active elsewhere?", resumeConfirmTitle(s))
	}
	return runResumeConfirm(title, buildResumeDetail(s, resolvedState, warn, time.Now()), !warn)
}

// runResumeConfirm renders a huh confirm form with esc bound to dismiss
// (huh's default keymap binds Quit to ctrl+c only). Returns (ok, nil)
// on a normal answer and (false, nil) when the user dismisses with
// esc/ctrl-c. Mirrors clown's runResumeConfirm.
func runResumeConfirm(title, description string, defaultYes bool) (bool, error) {
	ok := defaultYes
	confirm := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative("Resume").
		Negative("Cancel").
		Value(&ok)

	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"))

	form := huh.NewForm(huh.NewGroup(confirm)).
		WithKeyMap(km).
		WithShowHelp(false)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// isInteractiveTerminal reports whether stdin is a TTY, matching the
// check the sessionpick picker uses.
func isInteractiveTerminal() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
