package profilerights

import (
	"errors"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
)

// D1 V-D1-2 at spawn: a driver holding manager-builder may spawn a worker
// under engineer-reviewer, whose _base is manager-builder, and the minted
// link verifies through the same base chain.
const (
	stackBase       = "manager-builder"
	stackBaseDigest = "sha256-base"
	stackChild      = "engineer-reviewer"
	stackChildDig   = "sha256-child"
)

func stackingEnv(t *testing.T, chains map[string][]profilegrant.BaseLink) *env {
	t.Helper()
	e := newEnv(t)
	e.c.BaseChain = func(name, d string) ([]profilegrant.BaseLink, error) {
		chain, ok := chains[name+"@"+d]
		if !ok {
			return nil, errors.New("unknown definition")
		}
		return chain, nil
	}
	e.bless(e.card, driverPrincipal, map[string]string{stackBase: stackBaseDigest}, profilegrant.OnwardGrant, now.Add(time.Hour))
	return e
}

func TestAuthorizeAProfileHeldThroughItsBaseAndIssueItsLink(t *testing.T) {
	e := stackingEnv(t, map[string][]profilegrant.BaseLink{
		stackChild + "@" + stackChildDig: {{Name: stackBase, Digest: stackBaseDigest}},
	})
	a, err := e.c.Authorize(driverPrincipal, "", stackChild, stackChildDig)
	if err != nil {
		t.Fatalf("a profile stacked on a held base was refused: %v", err)
	}
	link, issued, err := e.c.IssueChildLink(a, driverPrincipal, "repo/worker", stackChild, stackChildDig)
	if err != nil || !issued {
		t.Fatalf("IssueChildLink: issued=%v err=%v", issued, err)
	}
	if cur, ok := e.store.Current("repo/worker"); !ok || cur.Digest != link.Digest {
		t.Fatal("the stacked link is not bound to the worker's session")
	}
	// The worker holds exactly its own profile, through its own link.
	e.principals["worker-principal"] = "repo/worker"
	w, err := e.c.Authorize("worker-principal", "repo/worker", stackChild, stackChildDig)
	if err != nil || w.MayPassOn {
		t.Fatalf("worker authorization: %+v, %v", w, err)
	}
}

func TestAuthorizeRefusesAStackedProfileThatWidensOrMisses(t *testing.T) {
	e := stackingEnv(t, map[string][]profilegrant.BaseLink{
		"widener@sha256-w":   {{Name: stackBase, Digest: stackBaseDigest, Widenings: []string{"adds server folio"}}},
		"stale@sha256-s":     {{Name: stackBase, Digest: "sha256-old"}},
		"unrelated@sha256-u": {{Name: "other", Digest: "sha256-o"}},
	})
	for name, c := range map[string]struct{ profile, digest, want string }{
		"widening":           {"widener", "sha256-w", "widens base manager-builder by: adds server folio"},
		"base at old digest": {"stale", "sha256-s", "reaches it at sha256-old"},
		"reaches nothing":    {"unrelated", "sha256-u", "reaches no profile the parent holds"},
		"unknown":            {"ghost", "sha256-g", "cannot be checked: unknown definition"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.c.Authorize(driverPrincipal, "", c.profile, c.digest)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

// Without a base-chain resolver nothing changes: a profile not held is refused
// with the list of what is held.
func TestAuthorizeWithoutABaseChainKeepsTheHeldRule(t *testing.T) {
	e := stackingEnv(t, nil)
	e.c.BaseChain = nil
	_, err := e.c.Authorize(driverPrincipal, "", stackChild, stackChildDig)
	if err == nil || !strings.Contains(err.Error(), "is not held; this spawner holds: manager-builder") {
		t.Fatalf("err = %v", err)
	}
}
