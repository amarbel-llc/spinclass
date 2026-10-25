package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"code.linenisgreat.com/spinclass/internal/alfa/grantstore"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/grantverify"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// profileResumePlan is the outcome of planRecordedProfile.
type profileResumePlan struct {
	// Entry is the resume entry to attach with (profile spliced in).
	Entry []string
	// RepinFrom/RepinTo are set only when --repin-profile accepted a digest
	// mismatch; the caller persists RepinTo after the attach decision.
	RepinFrom, RepinTo string
	// RepinToForm is the new definition's redacted form, for the re-pin's
	// field diff only (spawn.ResolvedProfile.RedactedForm).
	RepinToForm []byte
	// Warning is a non-fatal note for stderr (e.g. no digest recorded).
	Warning string
	// Notes are informational lines for stderr (e.g. the profile a shell
	// fallback cannot carry).
	Notes []string
	// Env is the profile pin for clown (spawn.ProfileLaunchEnv, #407): the
	// digest this resume approved and the session it is bound to. nil for a
	// session with no profile.
	Env []string
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
//
// shellFallback is true when no [session-entry].resume is configured and the
// start entry (a shell, by default) runs. A shell is not a harness, has no "--"
// to carry --profile, and the operator starts the harness from it; so the entry
// is left untouched and the plan names the profile instead. The definition
// check still applies.
func planRecordedProfile(
	st session.State,
	resolve func(string) (spawn.ResolvedProfile, error),
	entry []string,
	repin, shellFallback bool,
) (profileResumePlan, error) {
	if st.Profile == "" {
		return profileResumePlan{Entry: entry}, nil
	}

	// Check the separator first so the refusal names the resume entry rather
	// than a spawn-entry (SpliceProfileFlag's message is spawn-worded).
	if !shellFallback && !slices.Contains(entry, "--") {
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
		plan.RepinFrom, plan.RepinTo, plan.RepinToForm = st.ProfileDigest, rp.Digest, rp.RedactedForm
	}

	// rp is now the definition this resume accepts, so clown is pinned to its
	// digest and refuses to start on anything else. The resume entry passes the
	// recorded principal as --resume, the id the approval is bound to.
	if rp.DigestHex == "" {
		return profileResumePlan{}, fmt.Errorf("cannot resume: recorded profile %q resolved without a clown digest, so clown cannot be pinned to it", st.Profile)
	}
	plan.Env = spawn.ProfileLaunchEnv(rp.DigestHex, st.Principal)

	if shellFallback {
		plan.Entry = entry
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"session was spawned with profile %q (digest %s); no [session-entry].resume is configured, so the start entry %v runs as is: start the harness with --profile %s",
			st.Profile, rp.Digest, entry, st.Profile))
		return plan, nil
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
	// Approve obtains the card-signed profile transition for the change, whose
	// act text shows diff, and returns the rebind to record
	// (transitionApprover.Approve, #407).
	Approve func(profile, from, to string, diff []string) (session.ProfileRebind, error)
}

// authorizeRepin decides whether an accepted digest mismatch may be recorded.
// The flag alone is not authority: an agent that can run `sc resume` could
// otherwise silently re-pin a profile whose definition changed. A re-pin
// therefore needs an attached, interactive terminal and a card-signed profile
// transition for exactly this change, already stored or signed now.
func authorizeRepin(profile, from, to string, diff []string, g repinGate) (session.ProfileRebind, error) {
	if g.NoAttach {
		return session.ProfileRebind{}, errors.New("--repin-profile cannot be combined with --no-attach: a re-pin needs the operator at a terminal")
	}
	if !g.Interactive {
		return session.ProfileRebind{}, errors.New("--repin-profile needs an interactive terminal (stdin and stdout); refusing to re-pin unattended")
	}
	rb, err := g.Approve(profile, from, to, diff)
	if err != nil {
		return session.ProfileRebind{}, fmt.Errorf("profile %s re-pin not approved, resume refused: %w", profile, err)
	}
	return rb, nil
}

// applyRecordedProfile is the resume path between loading the sweatfile and
// attaching: plan the profile, and when the plan carries a re-pin, get it
// authorized and persist it before returning the entry to attach with.
// Persisting precedes Attach because Attach blocks for the session's life. env
// is the profile pin to add to the harness environment (profileResumePlan.Env).
func applyRecordedProfile(
	st session.State,
	resolve func(string) (spawn.ResolvedProfile, error),
	entry []string,
	repin, shellFallback bool,
	gate repinGate,
	persist func(session.State, session.ProfileRebind) error,
	stdout, stderr io.Writer,
) (attachEntry, env []string, err error) {
	plan, err := planRecordedProfile(st, resolve, entry, repin, shellFallback)
	if err != nil {
		return nil, nil, err
	}
	if plan.Warning != "" {
		fmt.Fprintln(stderr, "spinclass: "+plan.Warning)
	}
	for _, note := range plan.Notes {
		fmt.Fprintln(stderr, "spinclass: "+note)
	}
	if plan.RepinTo != "" {
		rb, err := authorizeRepin(st.Profile, plan.RepinFrom, plan.RepinTo, transitionDiffLines(plan.RepinFrom, plan.RepinToForm), gate)
		if err != nil {
			return nil, nil, err
		}
		if err := persist(st, rb); err != nil {
			return nil, nil, fmt.Errorf("re-pinning profile digest: %w", err)
		}
		fmt.Fprintf(stdout, "profile %s re-pinned from %s to %s (transition %s)\n", st.Profile, plan.RepinFrom, plan.RepinTo, rb.Transition)
	}
	return plan.Entry, plan.Env, nil
}

// withEnvPairs returns a copy of base with the KEY=VALUE pairs added, later
// pairs winning; base itself (the session's pinned env) is never modified.
func withEnvPairs(base map[string]string, pairs []string) map[string]string {
	if len(pairs) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(pairs))
	maps.Copy(out, base)
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// stdinStdoutAreTerminals: a re-pin confirmation must be visible and answerable.
func stdinStdoutAreTerminals() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// persistRepin records the redeemed transition in the session state: the new
// digest, and the rebind entry that is the session's lasting record of it.
func persistRepin(st session.State, rb session.ProfileRebind) error {
	return session.Update(st.RepoPath, st.Branch, func(s *session.State) error {
		s.ProfileDigest = rb.To
		s.ProfileRebinds = append(s.ProfileRebinds, rb)
		return nil
	})
}

// newProductionApprover wires transitionApprover to the operator's card, the
// trusted signers file and the grant store, with ttl the trusted
// [profile-rights].transition-ttl.
func newProductionApprover(ctx context.Context, ttl time.Duration) (transitionApprover, error) {
	keys, err := grantverify.LoadSigners(profileGrantSigners())
	if err != nil {
		return transitionApprover{}, fmt.Errorf("profile transitions are verified against the card's signers file: %w", err)
	}
	dir, err := profileGrantImportStoreDir()
	if err != nil {
		return transitionApprover{}, err
	}
	store, err := grantstore.Open(dir)
	if err != nil {
		return transitionApprover{}, err
	}
	return transitionApprover{
		Ctx: ctx, Now: time.Now, TTL: ttl, Store: store, Signers: keys,
		Signer:          profileGrantSigner,
		RequireOperator: func() error { return requireOperatorTerminalFor("sc resume --repin-profile") },
		Nonce:           randomNonce,
		ActOut:          os.Stderr,
	}, nil
}
