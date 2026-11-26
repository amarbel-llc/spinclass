package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"code.linenisgreat.com/clown/pkgs/bridgevectors"
	"code.linenisgreat.com/clown/pkgs/peercheck"
)

// The single home of the bridge's conformance vectors is this package's
// testdata. Clown vendors both files and records the sha256 it vendored as
// exported constants (bridgevectors.SHA256, peercheck.AdmissionVectorsSHA256).
// A vector change published here without a re-vendor in clown fails this test,
// so the two repositories cannot drift apart silently.

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVectorsDriftAgainstTheLockedClown(t *testing.T) {
	for _, c := range []struct {
		name, path, clown string
	}{
		{"vectors.json", "testdata/vectors.json", bridgevectors.SHA256},
		{"admission-vectors.json", "testdata/admission-vectors.json", peercheck.AdmissionVectorsSHA256},
	} {
		if got := fileSHA256(t, c.path); got != c.clown {
			t.Errorf("%s here is sha256 %s, the locked clown recorded %s: re-vendor %s in clown (and tell clown the landed commit), or bump the clown lock to the rev that re-vendored it", c.name, got, c.clown, c.name)
		}
	}
}
