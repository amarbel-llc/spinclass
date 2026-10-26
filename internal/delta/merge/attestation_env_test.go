package merge

import (
	"slices"
	"strings"
	"testing"
)

// #373: an attestation-admitted merge hands post-merge the claimed attestation's
// blob; any other merge has none, and the variable is absent.
func TestPostMergeEnvCarriesTheAttestationBlob(t *testing.T) {
	env := PostMergeEnv(PostMergeFacts{LandedSha: "abc", AttestationBlob: "madder://blobs/blake2b256-x"})
	if !slices.Contains(env, "SPINCLASS_ATTESTATION_BLOB=madder://blobs/blake2b256-x") {
		t.Errorf("env lacks the attestation blob: %q", env)
	}
	for _, kv := range PostMergeEnv(PostMergeFacts{LandedSha: "abc"}) {
		if strings.HasPrefix(kv, "SPINCLASS_ATTESTATION_BLOB=") {
			t.Errorf("a merge with no attestation set %q", kv)
		}
	}
}
