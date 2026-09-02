package sweatfile

import "testing"

func TestMergeWithAuthScalarOverride(t *testing.T) {
	mint, revoke, mint2 := "papi mint", "papi revoke", "other mint"
	base := Sweatfile{Auth: &Auth{MintCommand: &mint, RevokeCommand: &revoke}}

	merged := base.MergeWith(Sweatfile{Auth: &Auth{MintCommand: &mint2}})
	if got := merged.AuthMintCommand(); got == nil || *got != mint2 {
		t.Errorf("mint-command not overridden: %v", got)
	}
	if got := merged.AuthRevokeCommand(); got == nil || *got != revoke {
		t.Errorf("revoke-command not inherited: %v", got)
	}

	// No [auth] in the child inherits the parent's whole table.
	if got := base.MergeWith(Sweatfile{}).AuthMintCommand(); got == nil || *got != mint {
		t.Errorf("[auth] not inherited: %v", got)
	}
	if got := (Sweatfile{}).AuthMintCommand(); got != nil {
		t.Errorf("no [auth] anywhere: got %q", *got)
	}
}
