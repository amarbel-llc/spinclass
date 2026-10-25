package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"code.linenisgreat.com/piggy/go/pkgs/markl"
	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
	"code.linenisgreat.com/spinclass/internal/alfa/grantstore"
	"code.linenisgreat.com/spinclass/internal/alfa/operatoract"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
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

	rb, err := a.Approve("fable-design", trFrom, trTo, nil)
	if err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if card.calls != 1 || card.ns != operatoract.NamespaceEscalation {
		t.Fatalf("card asked %d times under %q, want once under %s", card.calls, card.ns, operatoract.NamespaceEscalation)
	}
	if !strings.HasPrefix(card.text, "spinclass profile transition (escalation): fable-design\n") {
		t.Errorf("the card was shown %q, want the transition act text", card.text)
	}
	// With no diff given (no stored old form), the operator is told the diff
	// is missing rather than shown digests alone silently.
	if !strings.Contains(card.text, "\nfield diff unavailable: the old definition is not recorded\n") {
		t.Errorf("the card text lacks the missing-diff line:\n%s", card.text)
	}
	if rb.From != trFrom || rb.To != trTo || rb.Transition == "" || rb.By == "" || !rb.At.Equal(testNow) {
		t.Errorf("rebind = %+v", rb)
	}
	if got := store.FindTransitions("fable-design", trFrom, trTo); len(got) != 1 || got[0].Digest != rb.Transition {
		t.Fatalf("stored transitions = %+v, want the signed one", got)
	}

	// A second session pinned to the same digest redeems the stored approval.
	second, err := testApprover(t, card, store, testNow.Add(time.Hour)).Approve("fable-design", trFrom, trTo, nil)
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
	if _, err := testApprover(t, card, store, testNow).Approve("fable-design", trFrom, trTo, nil); err != nil {
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
		if _, err := testApprover(t, card, store, c.now).Approve(c.profile, c.from, c.to, nil); err != nil {
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
	rb, err := testApprover(t, card, store, testNow).Approve("fable-design", trFrom, trTo, nil)
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
	if _, err := notOperator.Approve("fable-design", trFrom, trTo, nil); err == nil || !strings.Contains(err.Error(), "CLOWN_SESSION_ID") {
		t.Errorf("agent runtime: err = %v", err)
	}

	noSigners := testApprover(t, card, openStore(t), testNow)
	noSigners.Signers = nil
	if _, err := noSigners.Approve("fable-design", trFrom, trTo, nil); err == nil {
		t.Error("approved with no signers to verify against")
	}
	if card.calls != 0 {
		t.Errorf("the card was asked %d times; every refusal here must come first", card.calls)
	}
}

// profileDigestID renders a clown-profile-digest-v1 id over a 32-byte digest
// filled with b, as session state records it.
func profileDigestID(t *testing.T, b byte) (id, hexDigest string) {
	t.Helper()
	raw := bytes.Repeat([]byte{b}, 32)
	var m markl.Id
	if err := m.SetPurposeId(spawn.ProfileDigestPurpose); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMarklId(markl.FormatIdHashSha256, raw); err != nil {
		t.Fatal(err)
	}
	text, err := m.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return string(text), hex.EncodeToString(raw)
}

// redactedFormOf is clown's redacted form wire shape (profilelookup
// RedactedForm, form version 1) for a profile with model and env keys.
func redactedFormOf(model string, envKeys ...string) []byte {
	keys, _ := json.Marshal(append([]string{}, envKeys...))
	return []byte(`{"form":1,"protocol":2,"name":"lean","display":"","provider":"claude","backend":"","model":"` + model +
		`","url":"","token_set":false,"env_keys":` + string(keys) + `,"context_servers":null,"context_excluded":{},"base":""}`)
}

// stubRedactedForms stands in for clown's stored pins: forms by hex digest, or
// a read error for every digest when readErr is set.
func stubRedactedForms(t *testing.T, forms map[string][]byte, readErr error) {
	t.Helper()
	orig := lookupRedactedForm
	t.Cleanup(func() { lookupRedactedForm = orig })
	lookupRedactedForm = func(digest string) ([]byte, bool, error) {
		if readErr != nil {
			return nil, false, readErr
		}
		form, ok := forms[digest]
		return form, ok, nil
	}
}

// #407 item 2 golden: with clown's stored form of the old digest, the act text
// shows the limits note and clown's own diff lines, in clown's order.
func TestTransitionDiffLinesGolden(t *testing.T) {
	from, fromHex := profileDigestID(t, 0x11)
	stubRedactedForms(t, map[string][]byte{fromHex: redactedFormOf("haiku", "A")}, nil)
	got := transitionDiffLines(from, redactedFormOf("sonnet", "A", "B"))
	want := []string{
		"diff of recorded forms, unverified; env values, url paths/creds not shown",
		`model: "haiku" -> "sonnet"`,
		`env "B": added`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("diff lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := transitionDiffLines(from, redactedFormOf("haiku", "A")); !slices.Equal(got, []string{transitionDiffNote, noVisibleChanges}) {
		t.Errorf("identical forms: %q", got)
	}
}

// Every way the old form can be missing or unreadable keeps the interim line;
// the form never decides anything, so none of them is an error.
func TestTransitionDiffLinesFallsBackToTheMissingLine(t *testing.T) {
	from, fromHex := profileDigestID(t, 0x11)
	newForm := redactedFormOf("sonnet")
	for name, c := range map[string]struct {
		from    string
		forms   map[string][]byte
		readErr error
		newForm []byte
	}{
		"no stored form":       {from, nil, nil, newForm},
		"sidecar unreadable":   {from, nil, errors.New("permission denied"), newForm},
		"stored form garbled":  {from, map[string][]byte{fromHex: []byte("{not json")}, nil, newForm},
		"unknown form version": {from, map[string][]byte{fromHex: []byte(`{"form":99}`)}, nil, newForm},
		"no new form":          {from, map[string][]byte{fromHex: redactedFormOf("haiku")}, nil, nil},
		"not a profile digest": {"clown-profile-digest-v1@sha256-old", map[string][]byte{fromHex: redactedFormOf("haiku")}, nil, newForm},
	} {
		t.Run(name, func(t *testing.T) {
			stubRedactedForms(t, c.forms, c.readErr)
			if got := transitionDiffLines(c.from, c.newForm); !slices.Equal(got, missingDiff) {
				t.Errorf("got %q, want the missing-diff line", got)
			}
		})
	}
}

// Budget: however many fields changed, the note plus clown's capped lines fit
// MaxTransitionDiffLines, the last naming how many more there are, and the act
// text over real-length digests stays within its line and size budget.
func TestTransitionDiffLinesFitTheActTextBudget(t *testing.T) {
	from, fromHex := profileDigestID(t, 0x11)
	to, _ := profileDigestID(t, 0x22)
	stubRedactedForms(t, map[string][]byte{fromHex: redactedFormOf("haiku")}, nil)
	keys := []string{"K1", "K2", "K3", "K4", "K5", "K6", "K7", "K8"}
	lines := transitionDiffLines(from, redactedFormOf("sonnet", keys...))
	if len(lines) != profilegrant.MaxTransitionDiffLines {
		t.Fatalf("%d lines, want %d:\n%s", len(lines), profilegrant.MaxTransitionDiffLines, strings.Join(lines, "\n"))
	}
	// 9 changes, 5 lines for them: 4 shown and "+5 more changes".
	if last := lines[len(lines)-1]; last != "+5 more changes" {
		t.Errorf("last line %q, want the count of elided changes", last)
	}
	tr := profilegrant.Transition{Profile: "lean", From: from, To: to, Expires: testNow.Add(24 * time.Hour), Nonce: "00112233445566778899aabbccddeeff"}
	digest, err := profilegrant.DigestOf(tr.CanonicalText())
	if err != nil {
		t.Fatal(err)
	}
	act, err := profilegrant.TransitionActText(tr, digest, lines)
	if err != nil {
		t.Fatalf("the act text refused a full diff: %v", err)
	}
	for _, line := range lines {
		if !strings.Contains(act, "\n"+line+"\n") && !strings.HasSuffix(act, "\n"+line) {
			t.Errorf("act text lacks %q:\n%s", line, act)
		}
	}
}

// The approver puts the diff it is given into both the card's act text and
// the stored record.
func TestApproverShowsAndStoresTheGivenDiff(t *testing.T) {
	card := newTestCard(t)
	store := openStore(t)
	diff := []string{transitionDiffNote, `model: "haiku" -> "sonnet"`}
	rb, err := testApprover(t, card, store, testNow).Approve("fable-design", trFrom, trTo, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card.text, "\n"+strings.Join(diff, "\n")) || strings.Contains(card.text, missingDiff[0]) {
		t.Errorf("card text:\n%s", card.text)
	}
	got := store.FindTransitions("fable-design", trFrom, trTo)
	if len(got) != 1 || got[0].Digest != rb.Transition || !slices.Equal(got[0].Diff, diff) {
		t.Errorf("stored = %+v", got)
	}
}

func TestApproverStoresNothingWhenTheCardSignsWithAnotherKey(t *testing.T) {
	card, other := newTestCard(t), newTestCard(t)
	store := openStore(t)
	a := testApprover(t, other, store, testNow)
	a.Signers = []ssh.PublicKey{card.pub}
	if _, err := a.Approve("fable-design", trFrom, trTo, nil); err == nil {
		t.Fatal("approved a transition signed by a key not in the signers file")
	}
	if got := store.FindTransitions("fable-design", trFrom, trTo); len(got) != 0 {
		t.Errorf("stored an unverifiable transition: %+v", got)
	}
}
