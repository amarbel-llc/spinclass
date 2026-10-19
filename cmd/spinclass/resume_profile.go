package main

import (
	"fmt"
	"slices"

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

// persistRepin records the accepted digest in the session state.
func persistRepin(st session.State, to string) error {
	return session.Update(st.RepoPath, st.Branch, func(s *session.State) error {
		s.ProfileDigest = to
		return nil
	})
}
