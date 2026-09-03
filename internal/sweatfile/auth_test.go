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

// forge-hosts is an override array: nil inherits, a non-empty list replaces
// (never appends to) the inherited one, and an explicit [] clears it.
func TestMergeWithAuthForgeHostsOverride(t *testing.T) {
	mint := "papi mint"
	root := Sweatfile{Auth: &Auth{MintCommand: &mint, ForgeHosts: []string{"code.example.com"}}}

	if got := root.MergeWith(Sweatfile{Auth: &Auth{}}).AuthForgeHosts(); len(got) != 1 || got[0] != "code.example.com" {
		t.Errorf("nil forge-hosts did not inherit: %v", got)
	}
	got := root.MergeWith(Sweatfile{Auth: &Auth{ForgeHosts: []string{"other.example.com"}}}).AuthForgeHosts()
	if len(got) != 1 || got[0] != "other.example.com" {
		t.Errorf("non-empty forge-hosts did not replace: %v", got)
	}
	if got := root.MergeWith(Sweatfile{Auth: &Auth{ForgeHosts: []string{}}}).AuthForgeHosts(); len(got) != 0 {
		t.Errorf("[] did not clear forge-hosts: %v", got)
	}
	if got := root.AuthForgeHosts(); len(got) != 1 {
		t.Errorf("receiver mutated by merge: %v", got)
	}
}

func TestAllowNoCredentialScalarOverride(t *testing.T) {
	yes, no := true, false
	base := Sweatfile{Hooks: &Hooks{AllowNoCredential: &yes}}
	if !base.AllowNoCredential() {
		t.Error("allow-no-credential = true not reported")
	}
	if base.MergeWith(Sweatfile{Hooks: &Hooks{AllowNoCredential: &no}}).AllowNoCredential() {
		t.Error("child false did not override")
	}
	if (Sweatfile{}).AllowNoCredential() {
		t.Error("unset must be false")
	}
}
