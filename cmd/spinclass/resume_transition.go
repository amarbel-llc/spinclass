package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"

	"code.linenisgreat.com/clown/pkg/profilelookup"
	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
	"code.linenisgreat.com/spinclass/internal/alfa/grantstore"
	"code.linenisgreat.com/spinclass/internal/alfa/operatoract"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/grantverify"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// missingDiff stands in the act text's diff lines when no field diff can be
// shown: clown stored no redacted form for the old digest (a pin from before
// clown#298, a form over its size cap, another state dir) or it could not be
// read. The operator is told rather than left to infer it from two digests.
var missingDiff = []string{"field diff unavailable: the old definition is not recorded"}

// transitionDiffNote heads a real field diff. The forms are clown's redacted
// forms, read from a file anyone who can write clown's state dir could forge,
// and they hold no env values, URL paths or URL credentials, so such changes
// cannot show (clown#298). The diff is display only and never decides.
const transitionDiffNote = "diff of recorded forms, unverified; env values, url paths/creds not shown"

// noVisibleChanges is the diff line when the forms show no difference: the
// digests still differ, so the change is one the forms cannot hold.
const noVisibleChanges = "no field change is visible in the recorded forms"

// lookupRedactedForm reads the redacted form clown stored with a session pin
// of a digest, from the caller's $XDG_STATE_HOME. A variable for tests.
var lookupRedactedForm = profilelookup.RedactedFormByDigest

// transitionDiffLines is the act text's diff for re-pinning from the recorded
// digest from to a definition whose redacted form is toForm (#407 item 2):
// the note, then clown's DiffRedacted lines, within MaxTransitionDiffLines.
// Anything missing or unreadable yields missingDiff.
func transitionDiffLines(from string, toForm []byte) []string {
	fromHex, err := spawn.ProfileDigestHexOf(from)
	if err != nil || len(toForm) == 0 {
		return missingDiff
	}
	oldForm, ok, err := lookupRedactedForm(fromHex)
	if err != nil || !ok {
		return missingDiff
	}
	lines, _, err := profilelookup.DiffRedacted(oldForm, toForm, profilegrant.MaxTransitionDiffLines-1)
	if err != nil {
		return missingDiff
	}
	if len(lines) == 0 {
		lines = []string{noVisibleChanges}
	}
	return append([]string{transitionDiffNote}, lines...)
}

// rebindBy names who redeems a transition: --repin-profile runs only for the
// human at a terminal (repinGate), so it is the operator.
const rebindBy = "operator"

// transitionApprover is the authority behind `sc resume --repin-profile`
// (#407): a card-signed profile transition for exactly (profile, from, to),
// either one already in the store whose signature verifies and whose
// redemption window is open, or a fresh one the operator's card signs.
type transitionApprover struct {
	Ctx     context.Context
	Now     func() time.Time
	TTL     time.Duration
	Store   *grantstore.Store
	Signers []ssh.PublicKey
	Signer  func([]ssh.PublicKey) (operatoract.Signer, error)
	// RequireOperator refuses a card act an agent could have started
	// (requireOperatorTerminal); a stored approval needs no new act.
	RequireOperator func() error
	Nonce           func() (string, error)
	// ActOut receives the act text before the card is asked, so the operator
	// can compare it with the card's prompt.
	ActOut io.Writer
}

// Approve returns the rebind to record for the session. diff is the act text's
// field diff (transitionDiffLines); empty means missingDiff.
func (a transitionApprover) Approve(profile, from, to string, diff []string) (session.ProfileRebind, error) {
	if len(diff) == 0 {
		diff = missingDiff
	}
	if len(a.Signers) == 0 {
		return session.ProfileRebind{}, errors.New("no card signers to verify a profile transition against; refusing")
	}
	now := a.Now()
	root := grantverify.NewRootVerifier(a.Signers)
	for _, rec := range a.Store.FindTransitions(profile, from, to) {
		if _, err := profilegrant.VerifyTransition(now, rec, root); err == nil {
			return a.rebind(from, to, rec.Digest, now), nil
		}
	}

	if err := a.RequireOperator(); err != nil {
		return session.ProfileRebind{}, err
	}
	nonce, err := a.Nonce()
	if err != nil {
		return session.ProfileRebind{}, fmt.Errorf("profile transition nonce: %w", err)
	}
	tr := profilegrant.Transition{
		Profile: profile, From: from, To: to,
		Expires: now.UTC().Truncate(time.Second).Add(a.TTL),
		Nonce:   nonce,
	}
	text := tr.CanonicalText()
	digest, err := profilegrant.DigestOf(text)
	if err != nil {
		return session.ProfileRebind{}, err
	}
	act, err := profilegrant.TransitionActText(tr, digest, diff)
	if err != nil {
		return session.ProfileRebind{}, err
	}
	signer, err := a.Signer(a.Signers)
	if err != nil {
		return session.ProfileRebind{}, err
	}
	if a.ActOut != nil {
		fmt.Fprintf(a.ActOut, "Asking the operator card to sign this act (the card prompt shows the same text):\n\n%s\n", act)
	}
	sig, err := signer.Sign(a.Ctx, operatoract.NamespaceEscalation, act)
	if err != nil {
		return session.ProfileRebind{}, err
	}
	rec := profilegrant.TransitionRecord{Digest: digest, Text: text, Diff: diff, Sig: sig}
	if _, err := profilegrant.VerifyTransition(now, rec, root); err != nil {
		return session.ProfileRebind{}, fmt.Errorf("the signed profile transition does not verify, not using it: %w", err)
	}
	if err := a.Store.PutTransition(rec); err != nil {
		return session.ProfileRebind{}, err
	}
	return a.rebind(from, to, digest, now), nil
}

func (a transitionApprover) rebind(from, to, transition string, at time.Time) session.ProfileRebind {
	return session.ProfileRebind{From: from, To: to, Transition: transition, By: rebindBy, At: at.UTC()}
}

func randomNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
