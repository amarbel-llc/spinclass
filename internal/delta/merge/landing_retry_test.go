package merge

import "testing"

// A refused landing push is retried in place only when it lost a race: the
// target tip moved since the gate started AND the commit that was pushed does
// not already contain the new tip. Anything else (an auth or URL refusal,
// which leaves the tip where it was) fails as before.
func TestLandingLostRace(t *testing.T) {
	repo := setupRepo(t)
	base := runGit(t, repo, "rev-parse", "HEAD")

	runGit(t, repo, "commit", "--allow-empty", "-m", "landing")
	landing := runGit(t, repo, "rev-parse", "HEAD")

	runGit(t, repo, "checkout", "-q", "--detach", base)
	runGit(t, repo, "commit", "--allow-empty", "-m", "landed elsewhere during the gate")
	moved := runGit(t, repo, "rev-parse", "HEAD")

	cases := []struct {
		name                string
		tipBefore, tipAfter string
		want                bool
	}{
		{"tip unmoved is not a race", base, base, false},
		{"tip moved and not contained is a race", base, moved, true},
		{"tip moved but already contained is not a race", base, landing, false},
		{"unknown tip after the refusal is not a race", base, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := landingLostRace(repo, c.tipBefore, c.tipAfter, landing); got != c.want {
				t.Fatalf("landingLostRace(before=%s, after=%s) = %v, want %v", c.tipBefore, c.tipAfter, got, c.want)
			}
		})
	}
}

// The retry budget is a constant: two retries, so at most three gates.
func TestLandingRaceRetriesBound(t *testing.T) {
	if landingRaceRetries != 2 {
		t.Fatalf("landingRaceRetries = %d, want 2", landingRaceRetries)
	}
}
