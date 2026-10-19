package sweatfile

import "testing"

func TestMergeWithNeverCarriesProfileRights(t *testing.T) {
	on := true
	set := Sweatfile{ProfileRights: &ProfileRights{Enforce: &on}}

	if got := (Sweatfile{}).MergeWith(set); got.ProfileRights != nil {
		t.Errorf("merging in a [profile-rights] table carried it: %+v", got.ProfileRights)
	}
	// A receiver that already carries one (an unmerged layer) is stripped too.
	if got := set.MergeWith(Sweatfile{}); got.ProfileRights != nil {
		t.Errorf("receiver's [profile-rights] table survived the merge: %+v", got.ProfileRights)
	}
	if set.ProfileRights == nil {
		t.Error("MergeWith mutated the receiver")
	}
}
