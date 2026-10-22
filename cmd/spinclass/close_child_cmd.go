package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	spinclose "code.linenisgreat.com/spinclass/internal/close"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/servelog"
	"code.linenisgreat.com/spinclass/internal/session"
)

// emitExitWakesFn is a package-level seam over clown.EmitExitWakes (FDR 0032
// D6), shared by close_child_cmd.go and spawn_async.go, so their tests can
// assert invocation without a real or stubbed ringmaster on PATH —
// clown.EmitExitWakes itself gates on clown.Enabled(), which is false by
// construction in a plain test env.
var emitExitWakesFn = clown.EmitExitWakes

// closeChildParams is the parameter set of the `close-child-session` tool
// (#249): which spawned child to reap, and whether to override the safety
// refusal close.RunResolved raises for a child holding unmerged work.
type closeChildParams struct {
	Child string `json:"child"`
	Force bool   `json:"force"`
}

// authorizeHandleUse is the ONE authority predicate shared by
// close-child-session (reap), grant-session-handle, and
// release-session-handle: authorized iff callerPrincipal is non-empty AND
// (child.IsHolder(callerPrincipal) — FDR 0032 D12/D13's handle membership —
// OR callerKey is non-empty and equals child.SpawnedBy — the pre-FDR #249
// authority level, keyed on the driver's SESSION KEY rather than a
// principal).
//
// The second path is retained deliberately, and — unlike the FDR 0032 D9
// wording's "carries only SpawnedBy" framing might suggest — it applies even
// when SpawnedByPrincipal is ALSO set on the child: a `sc spawn` run from a
// plain shell (no CLOWN_SESSION_ID in the environment) records only a
// currentPrincipal per-process fallback UUID that dies with the process, and
// a `serve` restart mints a brand new one — so a driver that spawned a child
// yesterday from a plain shell has no way to reproduce today's fallback
// principal, and the durable session-key path is the only thing that still
// lets it reap/grant/release. This has to keep working until slice 1 binds
// identity cryptographically (a certificate chain, not an ambient env var) —
// see FDR 0032 D9.
//
// The session-key path ends when the spawner gives up its authority: a
// release (or a transfer, grant then release — FDR 0032 D12) by the spawner
// sets child.SpawnerReleased (runReleaseSessionHandle), and the key path then
// no longer authorizes. Holders membership is separate, so a spawner granted a
// handle again later still holds it. The marker lives in agent-writable
// state.json, so it binds only callers that cannot write the child's state
// (FDR 0032 D9).
//
// Returns a legible refusal naming the actual holder (noHandleMessage) when
// unauthorized, nil when authorized. Callers that want a richer,
// context-specific refusal (authorizeChildReap) use this only as an
// authorized/not-authorized signal and construct their own message on
// failure; callers with no better message of their own (grant, release)
// surface this error directly.
func authorizeHandleUse(callerPrincipal, callerKey string, child session.State) error {
	if callerPrincipal == "" {
		return fmt.Errorf(
			"could not resolve this session's principal, so ownership of %s cannot be established",
			child.Key(),
		)
	}
	if child.IsHolder(callerPrincipal) {
		return nil
	}
	if keyPathMatches(callerKey, child) {
		if child.SpawnerReleased {
			return errors.New(spawnerReleasedMessage(callerPrincipal, callerKey, child))
		}
		return nil
	}
	return errors.New(noHandleMessage(callerPrincipal, child))
}

// keyPathMatches reports whether the caller's session key is the child's
// recorded spawner key, the (releasable) session-key authority path.
func keyPathMatches(callerKey string, child session.State) bool {
	return callerKey != "" && child.SpawnedBy == callerKey
}

// callerHandleRights resolves the rights the caller holds on child (FDR 0032
// D13), assuming authorizeHandleUse already passed: a holder's resolved rights,
// else — the unreleased session-key spawner path — every right.
func callerHandleRights(callerPrincipal string, child session.State) session.HandleRightSet {
	if r, ok := child.RightsOf(callerPrincipal); ok && child.IsHolder(callerPrincipal) {
		return r
	}
	all, _ := session.ParseHandleRights(session.AllHandleRights)
	return all
}

// requireHandleRight refuses (principle 9: say what you hold, what is needed,
// who can grant it) unless the caller holds right on child. tool names the
// refusing tool in the message.
func requireHandleRight(tool, right, callerPrincipal string, child session.State) error {
	held := callerHandleRights(callerPrincipal, child)
	if held.Has(right) {
		return nil
	}
	with := "none"
	if h := child.HoldersWith(right); len(h) > 0 {
		with = strings.Join(h, ", ")
	}
	return fmt.Errorf(
		"%s refused: you hold %s on %s; %s needs %s; holders with it: %s; ask one to grant it, or run `sc close %s`",
		tool, held, child.Key(), tool, right, with, child.Key(),
	)
}

// spawnerReleasedMessage is the refusal for a caller whose only claim is the
// session-key path, which child.SpawnerReleased has ended: it says what the
// caller holds (nothing), who does hold the child, and the ways forward.
func spawnerReleasedMessage(callerPrincipal, callerKey string, child session.State) string {
	holders := "none"
	if len(child.Holders) > 0 {
		holders = strings.Join(child.Holders, ", ")
	}
	return fmt.Sprintf(
		"this session (principal %s, session key %s) released its handle on %s as its spawner, so the "+
			"session-key path no longer authorizes it; current holders: %s. "+
			"Ask a holder to grant a handle (grant-session-handle), or run `sc close %s`",
		callerPrincipal, callerKey, child.Key(), holders, child.Key(),
	)
}

// authorizeChildReap decides whether the caller may reap child (#249),
// building on authorizeHandleUse for the authorized/not-authorized decision
// but keeping its own richer, close-child-session-specific refusal wording
// (SpawnedBy/SpawnedByPrincipal are lineage, not authority: SpawnedBy is the
// `sc list` lineage column) — every existing refusal string here predates
// authorizeHandleUse's extraction and stays exactly as it read before.
//
// A session may reap ONLY a child it holds a handle on, so every uncertain
// case is a refusal: an unresolvable caller principal, a child with no
// lineage at all, or a child held by someone else. Failing open here would let
// any session close any other session in `sc list`, which is exactly the
// authority `sc close` deliberately reserves to the session itself and to the
// human.
func authorizeChildReap(callerPrincipal, callerKey string, child session.State) error {
	if callerPrincipal == "" {
		return fmt.Errorf(
			"could not resolve this session's principal, so ownership of %s cannot be established; "+
				"close-child-session only reaps sessions this one holds a handle on",
			child.Key(),
		)
	}
	if authorizeHandleUse(callerPrincipal, callerKey, child) == nil {
		return requireHandleRight("close-child-session", "close", callerPrincipal, child)
	}
	if keyPathMatches(callerKey, child) && child.SpawnerReleased {
		return errors.New(spawnerReleasedMessage(callerPrincipal, callerKey, child))
	}
	if child.SpawnedBy == "" && child.SpawnedByPrincipal == "" && len(child.Holders) == 0 {
		if callerKey != "" {
			return fmt.Errorf(
				"session %s carries no spawned_by lineage — it was not spawned by this session "+
					"(principal %s, session key %s) or any other; a session may only reap sessions it "+
					"holds a handle on. Close it from inside it, or run `sc close %s`",
				child.Key(), callerPrincipal, callerKey, child.Key(),
			)
		}
		return fmt.Errorf(
			"session %s carries no spawned_by lineage — it was not spawned by this session "+
				"(principal %s) or any other; a session may only reap sessions it holds a handle on. "+
				"Close it from inside it, or run `sc close %s`",
			child.Key(), callerPrincipal, child.Key(),
		)
	}
	spawner := child.EffectiveSpawner()
	return fmt.Errorf(
		"session %s was spawned by %s (spawned_by=%q, spawned_by_principal=%q), not by this session "+
			"(principal %s, session key %s); a session may only reap sessions it holds a handle on. "+
			"Ask %s to grant a handle (grant-session-handle), or run `sc close %s`",
		child.Key(), spawner, child.SpawnedBy, child.SpawnedByPrincipal, callerPrincipal, callerKey, spawner, child.Key(),
	)
}

// runCloseChild is the shared close-child-session flow: resolve the caller's
// own identity, resolve the named child, check the handle, and hand the reap
// to internal/close.
//
// Every safety check stays with close.RunResolved — it computes the child's
// unintegrated/dirty state itself and, finding no TTY to confirm on, refuses
// with a `--force` hint rather than blocking an MCP caller on a prompt. Force
// is therefore passed straight through and never pre-empted here.
func runCloseChild(p closeChildParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}

	// callerPrincipal always resolves (FDR 0032 D1); callerKey is best-effort
	// display-only, empty for a caller outside any worktree.
	callerPrincipal := currentPrincipal()
	callerKey := bestEffortSessionKey()

	child, err := session.FindByTarget(p.Child)
	if errors.Is(err, session.ErrTargetNotFound) {
		return "", fmt.Errorf(
			"no spinclass session for child %q; pass the <repo>/<branch> session key or worktree name `sc list` prints",
			p.Child,
		)
	}
	if err != nil {
		// Ambiguity (or index read failure): the error already carries the
		// disambiguating session keys.
		return "", err
	}

	// FDR 0032 handle guard: refuse before the accept-on-first-use write below
	// ever touches session.Write, which would either write a phantom
	// worktree-shaped state.json for an implicit (main-checkout) session or
	// fail with a raw "no such file" error for one already closed.
	if err := guardHandleTarget(child, "accept"); err != nil {
		return "", err
	}

	// A pending holder reaping the child exercises the handle it was granted —
	// accept-on-first-use (FDR 0032 D12) — BEFORE the authority check, so the
	// same call that first uses the handle is the one it authorizes.
	if child.IsPendingHolder(callerPrincipal) {
		child.AcceptHandle(callerPrincipal)
		if err := session.Write(*child); err != nil {
			return "", fmt.Errorf("accepting handle on %s: %w", child.Key(), err)
		}
	}

	if err := authorizeChildReap(callerPrincipal, callerKey, *child); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := spinclose.RunResolved(
		&buf, child.RepoPath, child.WorktreePath, child.Branch, p.Force, nil, "tap",
	); err != nil {
		return "", err
	}

	// FDR 0032 D6: a successful reap is the "shutdown" (or, forced, "killed")
	// exit-wake reason — every accepted holder OTHER than this caller (which
	// already knows: it is the one that just reaped). Best-effort: a wake
	// failure must not turn a completed reap into an error result.
	reason := clown.ExitShutdown
	if p.Force {
		reason = clown.ExitKilled
	}
	otherHolders := child.OtherHolders(callerPrincipal)
	if err := emitExitWakesFn(otherHolders, child.Key(), reason); err != nil {
		servelog.Errorf("close-child-session emitExitWakesFn-failed key=%s reason=%s err=%v", child.Key(), reason, err)
	}

	text := fmt.Sprintf("closed child session %s (worktree %s)", child.Key(), child.WorktreePath)
	if tapOut := strings.TrimSpace(buf.String()); tapOut != "" {
		text += "\n" + tapOut
	}
	return text, nil
}

// handleCloseChildSession is the `close-child-session` MCP tool handler. Its
// authority is a handle read from agent-writable session state, and `force`
// discards work, so a confined caller is refused before anything else.
func handleCloseChildSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("close-child-session", "sc close"); refusal != nil {
		return refusal, nil
	}
	var p closeChildParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runCloseChild(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// closeChildSessionParamList declares the `close-child-session` parameters.
func closeChildSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The child to reap, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "force",
			Type:        command.Bool,
			Description: "Reap even when the child has uncommitted changes, stash entries or commits not yet integrated into the default branch. Without it such a child is refused so its work is not silently discarded; a clean child with nothing to lose needs no force. Setting it always prompts the human — no allow-list can approve it silently — so reach for it only when the child's work is genuinely disposable.",
		},
	}
}
