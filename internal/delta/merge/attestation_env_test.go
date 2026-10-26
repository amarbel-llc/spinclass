package merge

import (
	"slices"
	"strings"
	"testing"
)

// #373: an attestation-admitted merge hands post-merge the claimed attestation's
// blob; any other merge has none, and the variable is absent.
func TestPostMergeEnvCarriesTheAttestationBlob(t *testing.T) {
	env := PostMergeEnv(PostMergeFacts{LandedSha: "abc", AttestationBlob: "madder://blobs/blake2b256-x", AttestationStore: "spinclass"})
	if !slices.Contains(env, "SPINCLASS_ATTESTATION_BLOB=madder://blobs/blake2b256-x") {
		t.Errorf("env lacks the attestation blob: %q", env)
	}
	if !slices.Contains(env, "SPINCLASS_ATTESTATION_STORE=spinclass") {
		t.Errorf("env lacks the attestation store: %q", env)
	}
	for _, kv := range PostMergeEnv(PostMergeFacts{LandedSha: "abc", AttestationStore: "spinclass"}) {
		if strings.HasPrefix(kv, "SPINCLASS_ATTESTATION_BLOB=") || strings.HasPrefix(kv, "SPINCLASS_ATTESTATION_STORE=") {
			t.Errorf("a merge with no attestation set %q", kv)
		}
	}
}

// OnLanded fires once with the landed sha, whatever the post-merge targets do
// (here there are none).
func TestOnLandedIsCalledOnceWithTheLandedSha(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	var got []string
	pm := PostMergeOptions{OnLanded: func(sha string) { got = append(got, sha) }}

	recs, err := runFinishWithMidEdit(t, repoDir, wtPath, "feature", false, pm, func() {})
	if err != nil {
		t.Fatalf("merge: %v (points %v)", err, testDescs(testRecords(recs)))
	}
	landed := runGit(t, repoDir, "rev-parse", "main")
	if len(got) != 1 || got[0] != landed {
		t.Errorf("OnLanded calls = %v, want exactly [%s]", got, landed)
	}
}
