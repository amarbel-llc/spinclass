package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"code.linenisgreat.com/spinclass/internal/grantstore"
	"code.linenisgreat.com/spinclass/internal/operatoract"
	"code.linenisgreat.com/spinclass/internal/profilegrant"
)

const (
	trFrom = "clown-profile-digest-v1@sha256-old"
	trTo   = "clown-profile-digest-v1@sha256-new"
)

func testApprover(t *testing.T, card *testCard, store *grantstore.Store, now time.Time) transitionApprover {
	t.Helper()
	return transitionApprover{
		Ctx:             context.Background(),
		Now:             func() time.Time { return now },
		TTL:             24 * time.Hour,
		Store:           store,
		Signers:         []ssh.PublicKey{card.pub},
		Signer:          func([]ssh.PublicKey) (operatoract.Signer, error) { return card, nil },
		RequireOperator: func() error { return nil },
		Nonce:           func() (string, error) { return "00112233445566778899aabbccddeeff", nil },
		ActOut:          io.Discard,
	}
}

func openStore(t *testing.T) *grantstore.Store {
	t.Helper()
	s, err := grantstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// With no stored approval the card signs a fresh transition, which is stored
// so later sessions pinned to the same digest need no second tap
// (clown FDR 0022 decision 5).
func TestApproverSignsStoresAndThenRedeemsWithoutATap(t *testing.T) {
	card := newTestCard(t)
	store := openStore(t)
	a := testApprover(t, card, store, testNow)

	rb, err := a.Approve("fable-design", trFrom, trTo)
	if err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if card.calls != 1 || card.ns != operatoract.NamespaceEscalation {
		t.Fatalf("card asked %d times under %q, want once under %s", card.calls, card.ns, operatoract.NamespaceEscalation)
	}
	if !strings.HasPrefix(card.text, "spinclass profile transition (escalation): fable-design\n") {
		t.Errorf("the card was shown %q, want the transition act text", card.text)
	}
	if rb.From != trFrom || rb.To != trTo || rb.Transition == "" || rb.By == "" || !rb.At.Equal(testNow) {
		t.Errorf("rebind = %+v", rb)
	}
	if got := store.FindTransitions("fable-design", trFrom, trTo); len(got) != 1 || got[0].Digest != rb.Transition {
		t.Fatalf("stored transitions = %+v, want the signed one", got)
	}

	// A second session pinned to the same digest redeems the stored approval.
	second, err := testApprover(t, card, store, testNow.Add(time.Hour)).Approve("fable-design", trFrom, trTo)
	if err != nil {
		t.Fatalf("redemption: %v", err)
	}
	if card.calls != 1 {
		t.Errorf("the card was asked again (%d calls); a stored approval must be redeemed", card.calls)
	}
	if second.Transition != rb.Transition {
		t.Errorf("redeemed %s, want the stored %s", second.Transition, rb.Transition)
	}
}

func TestApproverDoesNotRedeemAnotherChangeOrAClosedWindow(t *testing.T) {
	card := newTestCard(t)
	store := openStore(t)
	if _, err := testApprover(t, card, store, testNow).Approve("fable-design", trFrom, trTo); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		profile, from, to string
		now               time.Time
	}{
		"another profile": {"fable-review", trFrom, trTo, testNow},
		"another from":    {"fable-design", "clown-profile-digest-v1@sha256-older", trTo, testNow},
		"window closed":   {"fable-design", trFrom, trTo, testNow.Add(25 * time.Hour)},
	} {
		before := card.calls
		if _, err := testApprover(t, card, store, c.now).Approve(c.profile, c.from, c.to); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if card.calls != before+1 {
			t.Errorf("%s: the card was not asked; the stored approval must not apply", name)
		}
	}
}

// A stored record whose signature does not verify is not redeemed, and a fresh
// card act replaces it as the one used.
func TestApproverIgnoresAStoredTransitionThatDoesNotVerify(t *testing.T) {
	card := newTestCard(t)
	store := openStore(t)
	tr := profilegrant.Transition{Profile: "fable-design", From: trFrom, To: trTo, Expires: testNow.Add(time.Hour), Nonce: "forged"}
	text := tr.CanonicalText()
	digest, err := profilegrant.DigestOf(text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTransition(profilegrant.TransitionRecord{Digest: digest, Text: text, Sig: []byte("not a signature")}); err != nil {
		t.Fatal(err)
	}
	rb, err := testApprover(t, card, store, testNow).Approve("fable-design", trFrom, trTo)
	if err != nil {
		t.Fatal(err)
	}
	if card.calls != 1 || rb.Transition == digest {
		t.Errorf("redeemed the unverifiable record (card calls %d, transition %s)", card.calls, rb.Transition)
	}
}

func TestApproverRefusesBeforeTheCardWhenNotTheOperatorOrNoSigners(t *testing.T) {
	card := newTestCard(t)

	notOperator := testApprover(t, card, openStore(t), testNow)
	notOperator.RequireOperator = func() error { return errors.New("CLOWN_SESSION_ID is set") }
	if _, err := notOperator.Approve("fable-design", trFrom, trTo); err == nil || !strings.Contains(err.Error(), "CLOWN_SESSION_ID") {
		t.Errorf("agent runtime: err = %v", err)
	}

	noSigners := testApprover(t, card, openStore(t), testNow)
	noSigners.Signers = nil
	if _, err := noSigners.Approve("fable-design", trFrom, trTo); err == nil {
		t.Error("approved with no signers to verify against")
	}
	if card.calls != 0 {
		t.Errorf("the card was asked %d times; every refusal here must come first", card.calls)
	}
}

func TestApproverStoresNothingWhenTheCardSignsWithAnotherKey(t *testing.T) {
	card, other := newTestCard(t), newTestCard(t)
	store := openStore(t)
	a := testApprover(t, other, store, testNow)
	a.Signers = []ssh.PublicKey{card.pub}
	if _, err := a.Approve("fable-design", trFrom, trTo); err == nil {
		t.Fatal("approved a transition signed by a key not in the signers file")
	}
	if got := store.FindTransitions("fable-design", trFrom, trTo); len(got) != 0 {
		t.Errorf("stored an unverifiable transition: %+v", got)
	}
}
