package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// The key holder adds exactly one bind to the unit: the session's certificate
// DIRECTORY, read-only (a single-file bind would keep the old inode after the
// signer's rename), and one environment variable naming the file in it.
func TestBuildUnitSpecWithAHolderAddsOnlyTheCertificateDirectoryAsAReadOnlyBind(t *testing.T) {
	off := buildUnitSpec(offSpecInput())
	in := offSpecInput()
	in.holderSock = "/state/keyholder/h/agent/agent.sock"
	in.certDir = "/state/keyholder/h/cert"
	on := buildUnitSpec(in)

	var added []string
	for _, p := range on.BindsRO {
		if !slices.Contains(off.BindsRO, p) {
			added = append(added, p)
		}
	}
	if len(added) != 1 || added[0] != in.certDir {
		t.Errorf("read-only binds added by the key holder = %v, want exactly %s", added, in.certDir)
	}
	if !slices.Equal(on.BindsRW, off.BindsRW) {
		t.Errorf("the key holder changed the read-write binds: %v vs %v", on.BindsRW, off.BindsRW)
	}
	for _, p := range append(slices.Clone(on.BindsRO), on.BindsRW...) {
		for _, forbidden := range []string{"/state/keyholder/h", "/state/keyholder/h/agent", "/state/keyholder/h/fibby", "/state/keyholder/h/work"} {
			if p == forbidden {
				t.Errorf("the unit binds %s: the session directory, fibby's, the agent's and the work directory stay out", p)
			}
		}
	}
	if got, want := on.Env[keyholder.CertEnv], "/state/keyholder/h/cert/cert.pub"; got != want {
		t.Errorf("env %s = %q, want %q", keyholder.CertEnv, got, want)
	}
	if _, set := off.Env[keyholder.CertEnv]; set {
		t.Errorf("with the key holder off the spec sets %s", keyholder.CertEnv)
	}
}

func TestWriteUnitSpecBindsTheRealPairsCertificateDirectoryReadOnlyAndNothingElseOfItsDirectory(t *testing.T) {
	state, rp := agentCheckFixture(t)
	holder := writeHolderSpec(t, state, rp)
	spec, _ := readSpecForTest(t, state, rp.AbsPath)
	certDir := keyholder.CertDirOfAgentSock(holder)
	session := filepath.Dir(certDir)
	if !slices.Contains(spec.BindsRO, certDir) {
		t.Errorf("binds_ro %v lack the certificate directory %s", spec.BindsRO, certDir)
	}
	for _, p := range append(slices.Clone(spec.BindsRO), spec.BindsRW...) {
		if p != certDir && (p == session || strings.HasPrefix(p, session+"/")) {
			t.Errorf("a bind %s lies in the pair's directory beside the certificate directory", p)
		}
	}
	if spec.Env[keyholder.CertEnv] != filepath.Join(certDir, "cert.pub") {
		t.Errorf("env %s = %q", keyholder.CertEnv, spec.Env[keyholder.CertEnv])
	}
}

func TestWriteUnitSpecRefusesACertificateDirectoryThatIsMissingALinkOrAnotherPairs(t *testing.T) {
	state, rp := agentCheckFixture(t)
	holder := holderSocket(t)
	certDir := keyholder.CertDirOfAgentSock(holder)
	good := holderEnv(holder)

	cases := map[string]func() map[string]string{
		"no certificate path": func() map[string]string { return map[string]string{"SSH_AUTH_SOCK": holder} },
		"another pair's directory": func() map[string]string {
			other := holderSocket(t)
			return map[string]string{"SSH_AUTH_SOCK": holder, keyholder.CertEnv: filepath.Join(keyholder.CertDirOfAgentSock(other), "cert.pub")}
		},
		"not named cert.pub": func() map[string]string {
			return map[string]string{"SSH_AUTH_SOCK": holder, keyholder.CertEnv: filepath.Join(certDir, "other.pub")}
		},
		"missing directory": func() map[string]string {
			if err := os.Remove(certDir); err != nil {
				t.Fatal(err)
			}
			return good
		},
	}
	for name, mk := range cases {
		if path, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, mk(), BridgeUnset); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("%s: err = %v (spec %q), want a refusal naming the certificate", name, err, path)
		}
	}
	// a link in place of the directory
	holder2 := holderSocket(t)
	cd2 := keyholder.CertDirOfAgentSock(holder2)
	if err := os.Remove(cd2); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), cd2); err != nil {
		t.Fatal(err)
	}
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, holderEnv(holder2), BridgeUnset); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a linked certificate directory: err = %v, want 'not a directory'", err)
	}
	if _, err := os.Stat(confinement.UnitSpecPath(state, rp.AbsPath)); err == nil {
		t.Error("a refused launch left a spec on disk")
	}
}

// A stored holder spec written before the certificate interface lacks the bind:
// with the switch on it is a changed boundary, refused with the same answer as
// any other (close and spawn a new one).
func TestAStoredHolderSpecWithoutTheCertificateBindOrEnvIsRefusedWhenTheSwitchIsOn(t *testing.T) {
	state, rp := agentCheckFixture(t)
	holder := writeHolderSpec(t, state, rp)
	certDir := keyholder.CertDirOfAgentSock(holder)
	if err := CheckAgentBoundary(state, rp.AbsPath, true); err != nil {
		t.Fatalf("a current holder spec was refused: %v", err)
	}

	spec, _ := readSpecForTest(t, state, rp.AbsPath)
	spec.BindsRO = slices.DeleteFunc(slices.Clone(spec.BindsRO), func(p string) bool { return p == certDir })
	if _, err := confinement.WriteUnitSpec(state, rp.AbsPath, spec); err != nil {
		t.Fatal(err)
	}
	be := asBoundaryError(t, CheckAgentBoundary(state, rp.AbsPath, true))
	if !strings.Contains(be.Detail, "certificate directory") || !strings.Contains(be.Error(), "close this session and spawn a new one") {
		t.Errorf("no certificate bind: %v", be)
	}

	spec.BindsRO = append(spec.BindsRO, certDir)
	delete(spec.Env, keyholder.CertEnv)
	if _, err := confinement.WriteUnitSpec(state, rp.AbsPath, spec); err != nil {
		t.Fatal(err)
	}
	be = asBoundaryError(t, CheckAgentBoundary(state, rp.AbsPath, true))
	if !strings.Contains(be.Detail, keyholder.CertEnv) {
		t.Errorf("no certificate variable: %v", be)
	}
}
