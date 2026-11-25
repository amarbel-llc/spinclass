package main

import (
	"context"
	"fmt"
	"os"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// bridgePlan is what decides whether this serve listens as the session's
// bridge frontend. A plan with an empty Dir means no bridge, and Reason says
// why.
type bridgePlan struct {
	Dir    string
	Unit   string
	Reason string
}

// resolveBridgePlan turns the environment clown exported and the trusted
// [bridge] switch into a plan. Both must hold: the env names where and for
// which unit, and a layer above the repo says enable = true. A repo-layer
// [bridge] never counts.
func resolveBridgePlan(getenv func(string) string, home, repoRoot string) bridgePlan {
	dir, unit, ok := bridgeserver.EnvConfig(getenv)
	if !ok {
		return bridgePlan{Reason: "CLOWN_BRIDGE_DIR or CLOWN_BRIDGE_UNIT is unset: clown named no bridge for this session"}
	}
	h, err := sweatfileio.LoadHierarchy(home, repoRoot)
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("sweatfile hierarchy: %v", err)}
	}
	enable, untrusted := sweatfileio.TrustedBridgeEnabled(h, home, repoRoot)
	for _, p := range untrusted {
		servelog.Errorf("serve.bridge: [bridge] in %s is ignored: only sweatfiles above the repo may enable the bridge", p)
	}
	if !enable {
		return bridgePlan{Reason: "no trusted [bridge] enable = true"}
	}
	return bridgePlan{Dir: dir, Unit: unit}
}

// bridgeResolve is the seam tests replace for the working-directory and home
// reads behind the plan.
var bridgeResolve = func() bridgePlan {
	cwd, err := os.Getwd()
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("working directory unresolved: %v", err)}
	}
	repoRoot, err := git.CommonDir(cwd)
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("not in a git checkout, so no sweatfile hierarchy to read: %v", err)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("home directory unresolved: %v", err)}
	}
	return resolveBridgePlan(os.Getenv, home, repoRoot)
}

// startBridge starts the bridge listener when the plan allows it and returns
// what stops it; nil when serve runs without it, with the reason in the serve
// log. It never fails serve.
func startBridge(ctx context.Context) (stop func()) {
	plan := bridgeResolve()
	if plan.Dir == "" {
		servelog.Infof("serve.bridge off: %s", plan.Reason)
		return nil
	}
	key, err := currentSessionKey()
	if err != nil {
		servelog.Errorf("serve.bridge off: session key unresolved: %v", err)
		return nil
	}
	return startBridgeFrom(ctx, plan, bridgeserver.Options{SessionKey: key, Logf: servelog.Errorf})
}

// startBridgeFrom listens per the plan with opts and serves until the
// returned stop, which also removes the socket. Any failure logs one line and
// returns nil.
func startBridgeFrom(ctx context.Context, plan bridgePlan, opts bridgeserver.Options) (stop func()) {
	if plan.Dir == "" {
		return nil
	}
	s, err := bridgeserver.Listen(plan.Dir, plan.Unit, opts)
	if err != nil {
		servelog.Errorf("serve.bridge off: %v", err)
		return nil
	}
	servelog.Infof("serve.bridge listening path=%s unit=%s session=%s", s.Path(), plan.Unit, opts.SessionKey)
	done := make(chan struct{})
	go func() {
		_ = s.Serve(ctx)
		close(done)
	}()
	return func() {
		_ = s.Close()
		<-done
	}
}
