package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/clown/pkgs/peercheck"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/policybroker"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/bravo/folioeval"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// policyBrokerPlan is what decides whether this serve listens as the
// session's policy broker (spinclass#438, design record section 3) and with
// what. A plan with an empty Path means no broker, and Reason says why.
type policyBrokerPlan struct {
	Path       string
	SessionKey string
	Policy     sweatfileio.FolioPolicy
	Untrusted  []string
	RepoRoot   string
	Worktree   string
	StateDir   string
	Reason     string
}

// policyBrokerResolve is the seam tests replace for the environment and
// sweatfile reads behind the plan.
var policyBrokerResolve = resolvePolicyBroker

// resolvePolicyBroker reads what clown exported and what the trusted
// sweatfile layers say. Every failure to resolve is a plan without a broker:
// serve then runs as before and moxy keeps its static floor, which never
// widens anything.
func resolvePolicyBroker() policyBrokerPlan {
	addr := strings.TrimSpace(os.Getenv(policybroker.EnvAddr))
	if addr == "" {
		return policyBrokerPlan{Reason: policybroker.EnvAddr + " is unset: clown named no broker socket for this session"}
	}
	if !filepath.IsAbs(addr) {
		return policyBrokerPlan{Reason: fmt.Sprintf("%s=%q is not an absolute socket path; serve listens only on the path clown names (the host:port form is the in-unit relay's, never serve's)", policybroker.EnvAddr, addr)}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return policyBrokerPlan{Reason: fmt.Sprintf("working directory unresolved: %v", err)}
	}
	repoRoot, err := git.CommonDir(cwd)
	if err != nil {
		return policyBrokerPlan{Reason: fmt.Sprintf("not in a git checkout, so no sweatfile hierarchy to read: %v", err)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return policyBrokerPlan{Reason: fmt.Sprintf("home directory unresolved: %v", err)}
	}
	h, err := sweatfileio.LoadHierarchy(home, repoRoot)
	if err != nil {
		return policyBrokerPlan{Reason: fmt.Sprintf("sweatfile hierarchy: %v", err)}
	}
	policy, untrusted := sweatfileio.TrustedFolio(h, home, repoRoot)
	key, err := currentSessionKey()
	if err != nil {
		return policyBrokerPlan{Reason: fmt.Sprintf("session key unresolved: %v", err)}
	}
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return policyBrokerPlan{Reason: err.Error()}
	}
	return policyBrokerPlan{Path: addr, SessionKey: key, Policy: policy, Untrusted: untrusted, RepoRoot: repoRoot, Worktree: cwd, StateDir: stateDir}
}

// startPolicyBroker starts the broker listener when clown named a socket and
// a trusted [folio] policy is configured, and returns what stops it; nil
// when serve does not listen, with the reason in the serve log. It must run
// before the stdio server answers its first request: clown's bridge adds the
// confined relay only for a socket that exists when the health check passes.
func startPolicyBroker(ctx context.Context, version string) (stop func()) {
	plan := policyBrokerResolve()
	if plan.Path == "" {
		servelog.Infof("serve.broker off: %s", plan.Reason)
		return nil
	}
	for _, p := range plan.Untrusted {
		servelog.Errorf("serve.broker: [folio] allow or unmatched in %s is ignored: only sweatfiles above the repo may widen what a session reaches", p)
	}
	if !plan.Policy.Configured {
		servelog.Infof("serve.broker off: no trusted [folio] stanza for %s; moxy keeps its static floor", plan.RepoRoot)
		return nil
	}
	ev, err := newFolioEvaluator(plan)
	if err != nil {
		servelog.Errorf("serve.broker off: the [folio] policy is refused, so moxy keeps its static floor: %v", err)
		return nil
	}
	b, err := policybroker.Listen(policybroker.Options{
		Path:       plan.Path,
		SessionKey: plan.SessionKey,
		BrokerName: "spinclass serve " + version,
		Evaluator:  ev,
		PeerCheck:  policyBrokerPeerCheck(plan.StateDir, plan.Worktree),
		Logf:       servelog.Errorf,
	})
	if err != nil {
		servelog.Errorf("serve.broker off: %v", err)
		return nil
	}
	servelog.Infof("serve.broker listening path=%s session=%s unmatched=%s", plan.Path, plan.SessionKey, plan.Policy.Unmatched)
	done := make(chan struct{})
	go func() {
		b.Serve(ctx)
		close(done)
	}()
	return func() {
		_ = b.Close()
		<-done
	}
}

// policyBrokerPeerCheck admits a peer that runs as this uid and, for a
// session launched confined, runs in the session's agent unit (the in-unit
// relay, clown#333). A confined launch whose unit record cannot be read
// admits nobody: the record is what names the unit.
func policyBrokerPeerCheck(stateDir, worktree string) func(net.Conn) error {
	return func(c net.Conn) error {
		pid, err := policybroker.SameUID(c)
		if err != nil {
			return err
		}
		if !confinement.LaunchedConfined(stateDir, worktree) {
			return nil
		}
		rec, err := confinement.LoadUnitRecord(confinement.Host{}, stateDir, worktree)
		if err != nil {
			return fmt.Errorf("session launched confined but its unit is not recorded yet, so peer pid %d cannot be placed: %w", pid, err)
		}
		return peercheck.CheckUnitCgroup(pid, rec.Unit, nil)
	}
}

// policyBrokerSessions is the session index the evaluator reads held
// children from (the spinclass:children anchor). A seam for tests.
var policyBrokerSessions = func() []session.State {
	all, err := session.ListAll(nil)
	if err != nil {
		return nil
	}
	return all
}

// rootsTTL bounds how often the evaluator re-reads the session index for the
// children anchor: a check has a budget of tens of milliseconds, and a
// handle granted a second ago is soon enough.
const rootsTTL = time.Second

// folioEvaluator answers the broker's checks from the compiled policy and
// the session's roots; a blob resource is answered by the madder: anchor
// (design record section 5, ruled 2026-11-02).
type folioEvaluator struct {
	policy *folioeval.Policy
	plan   policyBrokerPlan
	home   string

	mu    sync.Mutex
	roots folioeval.Roots
	at    time.Time
}

// newFolioEvaluator compiles the trusted policy into the broker's evaluator.
// Every entry must parse and unmatched must be set: a policy the broker
// cannot compile is refused whole, never partly applied, since a dropped
// deny would widen it.
func newFolioEvaluator(plan policyBrokerPlan) (policybroker.Evaluator, error) {
	policy, err := folioeval.Compile(plan.Policy)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	return &folioEvaluator{policy: policy, plan: plan, home: home}, nil
}

func (e *folioEvaluator) Evaluate(_ context.Context, c policybroker.Check) (string, string, error) {
	switch c.Resource.Kind {
	case policybroker.KindFile:
		path := strings.TrimPrefix(c.Resource.URI, "file://")
		if !filepath.IsAbs(path) {
			return e.policy.Unmatched, "unmatched: resource is not an absolute file path", nil
		}
		verdict, reason := e.policy.Evaluate(e.currentRoots(), c.Op, folioeval.Resolve(path))
		return verdict, reason, nil
	case policybroker.KindBlob:
		verdict, reason := e.policy.EvaluateBlob(c.Op, c.Resource.URI)
		return verdict, reason, nil
	}
	return e.policy.Unmatched, "unmatched: no pattern grammar for " + c.Resource.Kind + " resources", nil
}

// homeForSubject is what the home: anchor expands to: the unit's HOME for a
// session launched confined (the agent-home under its worktree, the same path
// in the frontend and in the unit), the serve's own HOME otherwise
// (spinclass#443, operator ruling 2026-11-02). env: is not mapped: the unit's
// environment is clown's to compute, so env:NAME: stays the serve's.
func (e *folioEvaluator) homeForSubject() string {
	if confinement.LaunchedConfined(e.plan.StateDir, e.plan.Worktree) {
		return confinement.AgentHomeOf(e.plan.Worktree)
	}
	return e.home
}

func (e *folioEvaluator) currentRoots() folioeval.Roots {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.at.IsZero() && time.Since(e.at) < rootsTTL {
		return e.roots
	}
	e.roots = folioeval.RootsFor(e.plan.Worktree, e.plan.RepoRoot, e.homeForSubject(), os.Getenv("CLOWN_SESSION_ID"), os.Getenv, policyBrokerSessions())
	e.at = time.Now()
	return e.roots
}
