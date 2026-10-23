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

	"code.linenisgreat.com/spinclass/internal/grantstore"
	"code.linenisgreat.com/spinclass/internal/grantverify"
	"code.linenisgreat.com/spinclass/internal/operatoract"
	"code.linenisgreat.com/spinclass/internal/profilegrant"
	"code.linenisgreat.com/spinclass/internal/session"
)

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

// Approve returns the rebind to record for the session.
func (a transitionApprover) Approve(profile, from, to string) (session.ProfileRebind, error) {
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
	// The field diff (clown's profilelookup.Diff) is added once spinclass links
	// a clown that has it (#407 item 2); until then the text shows the digests.
	act, err := profilegrant.TransitionActText(tr, digest, nil)
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
	rec := profilegrant.TransitionRecord{Digest: digest, Text: text, Sig: sig}
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
