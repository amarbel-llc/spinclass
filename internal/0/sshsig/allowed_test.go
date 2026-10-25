package sshsig

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func pubLine(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestAllowedSignersKeysFor(t *testing.T) {
	ed, ec := pubLine(t, "ed25519.pub"), pubLine(t, "ecdsa.pub")
	src := strings.Join([]string{
		"# a comment",
		"",
		"alice,bob namespaces=\"piggy-escalation-v1,piggy-certificate-v1\" " + ed,
		"  * namespaces=\"only-this\" " + ec + " trailing comment words",
		"carol " + ec,
	}, "\n")
	a, err := ParseAllowedSigners(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	edKey, ecKey := fixtureKey(t, "ed25519.pub"), fixtureKey(t, "ecdsa.pub")

	cases := []struct {
		principal, namespace string
		want                 []ssh.PublicKey
	}{
		{"alice", "piggy-escalation-v1", []ssh.PublicKey{edKey}},
		{"bob", "piggy-certificate-v1", []ssh.PublicKey{edKey}},
		{"alice", "other", nil},
		{"alice", "only-this", []ssh.PublicKey{ecKey}}, // via the "*" principal
		{"mallory", "only-this", []ssh.PublicKey{ecKey}},
		{"mallory", "piggy-escalation-v1", nil},
		{"carol", "anything-at-all", []ssh.PublicKey{ecKey}}, // no namespaces option
		{"carol", "only-this", []ssh.PublicKey{ecKey, ecKey}},
		{"carol", "", nil}, // empty namespace never matches
	}
	for _, c := range cases {
		got := a.KeysFor(c.principal, c.namespace)
		if len(got) != len(c.want) {
			t.Errorf("KeysFor(%q,%q): got %d keys, want %d", c.principal, c.namespace, len(got), len(c.want))
			continue
		}
		for i := range got {
			if string(got[i].Marshal()) != string(c.want[i].Marshal()) {
				t.Errorf("KeysFor(%q,%q)[%d]: wrong key", c.principal, c.namespace, i)
			}
		}
	}
}

func TestKeysForStrict(t *testing.T) {
	ed, ec := pubLine(t, "ed25519.pub"), pubLine(t, "ecdsa.pub")
	src := strings.Join([]string{
		// no namespaces option, wildcard principal, the one explicit entry, wrong namespace
		"operator " + ed,
		"* namespaces=\"ns\" " + ec,
		"operator,other namespaces=\"ns\" " + ec,
		"operator namespaces=\"different\" " + ed,
	}, "\n")
	a, err := ParseAllowedSigners(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	got := a.KeysForStrict("operator", "ns")
	if len(got) != 1 || string(got[0].Marshal()) != string(fixtureKey(t, "ecdsa.pub").Marshal()) {
		t.Fatalf("strict match = %d keys, want exactly the explicit entry", len(got))
	}
	if len(a.KeysFor("operator", "ns")) < 3 {
		t.Fatal("KeysFor must keep its permissive semantics")
	}
	if a.KeysForStrict("*", "ns") != nil || a.KeysForStrict("operator", "") != nil || a.KeysForStrict("stranger", "ns") != nil {
		t.Fatal("wildcard principal, empty namespace and unknown principal match nothing")
	}
}

func TestAllowedSignersFeedsVerify(t *testing.T) {
	a, err := ParseAllowedSigners(strings.NewReader("op namespaces=\"" + fixtureNamespace + "\" " + pubLine(t, "ecdsa.pub") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = Verify(readFixture(t, "ecdsa.sig"), readFixture(t, "message.txt"), fixtureNamespace, a.KeysFor("op", fixtureNamespace))
	if err != nil {
		t.Fatal(err)
	}
}

func TestAllowedSignersTabSeparated(t *testing.T) {
	a, err := ParseAllowedSigners(strings.NewReader("alice\tnamespaces=\"ns\" " + pubLine(t, "ed25519.pub") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.KeysFor("alice", "ns")) != 1 {
		t.Fatal("tab-separated principal not parsed as plain \"alice\"")
	}
}

func TestAllowedSignersRejects(t *testing.T) {
	ed := pubLine(t, "ed25519.pub")
	cases := map[string]struct{ line, want string }{
		"cert-authority":    {"alice cert-authority " + ed, `"cert-authority"`},
		"valid-before":      {`alice valid-before="20300101" ` + ed, `"valid-before"`},
		"valid-after":       {`alice valid-after="20200101" ` + ed, `"valid-after"`},
		"unknown option":    {"alice frobnicate " + ed, `"frobnicate"`},
		"namespaces+extra":  {`alice namespaces="a",cert-authority ` + ed, `"cert-authority"`},
		"unquoted ns":       {"alice namespaces=a " + ed, "must be quoted"},
		"empty ns entry":    {`alice namespaces="a,,b" ` + ed, "namespace pattern"},
		"glob ns":           {`alice namespaces="piggy-*" ` + ed, "namespace pattern"},
		"duplicate ns":      {`alice namespaces="a",namespaces="b" ` + ed, "duplicate"},
		"glob principal":    {"ali* " + ed, "principal pattern"},
		"negated principal": {"!alice " + ed, "principal pattern"},
		"empty principal":   {"alice,,bob " + ed, "principal pattern"},
		"principals only":   {"alice", "expected principals"},
		"no key":            {"alice namespaces=\"a\"", "parsing key"},
		"bad key":           {"alice ssh-ed25519 AAAA", "parsing key"},
		"tab then option":   {"alice\tcert-authority " + ed, `"cert-authority"`},
		"unsupported type":  {"alice ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7", "parsing key"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAllowedSigners(strings.NewReader("# header\n" + c.line + "\n"))
			wantErr(t, err, c.want)
			wantErr(t, err, "line 2")
		})
	}
}
