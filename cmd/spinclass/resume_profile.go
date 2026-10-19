package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// profileResumePlan is the outcome of planRecordedProfile.
type profileResumePlan struct {
	// Entry is the resume entry to attach with (profile spliced in).
	Entry []string
	// RepinFrom/RepinTo are set only when --repin-profile accepted a digest
	// mismatch; the caller persists RepinTo after the attach decision.
	RepinFrom, RepinTo string
	// Warning is a non-fatal note for stderr (e.g. no digest recorded).
	Warning string
}

// planRecordedProfile decides how a session spawned with a clown profile
// resumes (FDR 0034, "Visibility and record"): re-apply the profile to the
// resume entry, but fail closed if the profile cannot be resolved or its
// definition changed since spawn. A session with no recorded profile returns
// the entry untouched without consulting resolve.
//
// repin is the human-authority override for a digest mismatch only; it must
// stay a CLI flag (an agent-held authority to re-pin arrives with the grant
// machinery in a later slice). Resolution failures always refuse.
func planRecordedProfile(
	st session.State,
	resolve func(string) (spawn.ResolvedProfile, error),
	entry []string,
	repin bool,
) (profileResumePlan, error) {
	if st.Profile == "" {
		return profileResumePlan{Entry: entry}, nil
	}

	// Check the separator first so the refusal names the resume entry rather
	// than a spawn-entry (SpliceProfileFlag's message is spawn-worded).
	if !slices.Contains(entry, "--") {
		return profileResumePlan{}, fmt.Errorf(
			"session was spawned with profile %q but the resume entry %v has no \"--\" provider-args separator to place --profile before; the recorded profile cannot be re-applied, refusing to resume without it",
			st.Profile, entry,
		)
	}

	rp, err := resolve(st.Profile)
	if err != nil {
		return profileResumePlan{}, fmt.Errorf("cannot resume: recorded profile %q failed to resolve: %w", st.Profile, err)
	}

	plan := profileResumePlan{}
	switch {
	case st.ProfileDigest == "":
		plan.Warning = fmt.Sprintf("session recorded no digest for profile %q (it predates digests); re-applying without a definition check", st.Profile)
	case rp.Digest != st.ProfileDigest:
		if !repin {
			return profileResumePlan{}, fmt.Errorf(
				"profile %s changed since this session was spawned (recorded %s, now %s); resume refused until an authority re-pins it",
				st.Profile, st.ProfileDigest, rp.Digest,
			)
		}
		plan.RepinFrom, plan.RepinTo = st.ProfileDigest, rp.Digest
	}

	plan.Entry, err = spawn.SpliceProfileFlag(entry, st.Profile)
	if err != nil {
		return profileResumePlan{}, err
	}
	return plan, nil
}

// repinGate is what a human-authority re-pin needs besides the flag itself.
type repinGate struct {
	// NoAttach: `sc resume --no-attach` is how scripts and agents resume
	// unattended, so it can never carry a re-pin.
	NoAttach bool
	// Interactive: stdin and stdout are terminals.
	Interactive bool
	// Confirm asks the operator; it must default to No.
	Confirm func(profile, from, to string) (bool, error)
}

// authorizeRepin decides whether an accepted digest mismatch may be recorded.
// The flag alone is not authority: an agent that can run `sc resume` could
// otherwise silently re-pin a profile whose definition changed. A re-pin
// therefore needs an attached, interactive terminal and an explicit yes that
// shows what is being accepted.
func authorizeRepin(profile, from, to string, g repinGate) error {
	if g.NoAttach {
		return errors.New("--repin-profile cannot be combined with --no-attach: a re-pin needs the operator to confirm at a terminal")
	}
	if !g.Interactive {
		return errors.New("--repin-profile needs an interactive terminal (stdin and stdout) to confirm; refusing to re-pin unattended")
	}
	ok, err := g.Confirm(profile, from, to)
	if err != nil {
		return fmt.Errorf("confirming profile re-pin: %w", err)
	}
	if !ok {
		return fmt.Errorf("profile %s re-pin declined; resume refused", profile)
	}
	return nil
}

// applyRecordedProfile is the resume path between loading the sweatfile and
// attaching: plan the profile, and when the plan carries a re-pin, get it
// authorized and persist it before returning the entry to attach with.
// Persisting precedes Attach because Attach blocks for the session's life.
func applyRecordedProfile(
	st session.State,
	resolve func(string) (spawn.ResolvedProfile, error),
	entry []string,
	repin bool,
	gate repinGate,
	persist func(session.State, string) error,
	stdout, stderr io.Writer,
) ([]string, error) {
	plan, err := planRecordedProfile(st, resolve, entry, repin)
	if err != nil {
		return nil, err
	}
	if plan.Warning != "" {
		fmt.Fprintln(stderr, "spinclass: "+plan.Warning)
	}
	if plan.RepinTo != "" {
		if err := authorizeRepin(st.Profile, plan.RepinFrom, plan.RepinTo, gate); err != nil {
			return nil, err
		}
		if err := persist(st, plan.RepinTo); err != nil {
			return nil, fmt.Errorf("re-pinning profile digest: %w", err)
		}
		fmt.Fprintf(stdout, "profile %s re-pinned from %s to %s\n", st.Profile, plan.RepinFrom, plan.RepinTo)
	}
	return plan.Entry, nil
}

// stdinStdoutAreTerminals: a re-pin confirmation must be visible and answerable.
func stdinStdoutAreTerminals() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// confirmRepinInteractively shows the profile and both digests and asks, with
// No as the default.
func confirmRepinInteractively(profile, from, to string) (bool, error) {
	ok := false
	confirm := huh.NewConfirm().
		Title(fmt.Sprintf("Re-pin profile %q?", profile)).
		Description(fmt.Sprintf("The profile definition changed since this session was spawned.\n\n  recorded: %s\n  now:      %s\n\nAccepting trusts the new definition for this session.", from, to)).
		Affirmative("Re-pin").
		Negative("Cancel").
		Value(&ok)
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"))
	if err := huh.NewForm(huh.NewGroup(confirm)).WithKeyMap(km).WithShowHelp(false).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// persistRepin records the accepted digest in the session state.
func persistRepin(st session.State, to string) error {
	return session.Update(st.RepoPath, st.Branch, func(s *session.State) error {
		s.ProfileDigest = to
		return nil
	})
}
