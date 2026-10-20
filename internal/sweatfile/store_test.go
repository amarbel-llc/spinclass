package sweatfile

import "testing"

func strPtr(s string) *string { return &s }

func TestStoreIdentityDefaultsCommitterToAuthor(t *testing.T) {
	for _, tc := range []struct {
		name        string
		store       *Store
		authorName  string
		authorEmail string
		comName     string
		comEmail    string
	}{
		{"unset", nil, "", "", "", ""},
		{"empty table", &Store{}, "", "", "", ""},
		{"author only", &Store{AuthorName: strPtr("A"), AuthorEmail: strPtr("a@x")}, "A", "a@x", "A", "a@x"},
		{"committer overrides one field", &Store{AuthorName: strPtr("A"), AuthorEmail: strPtr("a@x"), CommitterEmail: strPtr("c@x")}, "A", "a@x", "A", "c@x"},
		{"committer only", &Store{CommitterName: strPtr("C")}, "", "", "C", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			an, ae, cn, ce := Sweatfile{Store: tc.store}.StoreIdentity()
			if an != tc.authorName || ae != tc.authorEmail || cn != tc.comName || ce != tc.comEmail {
				t.Errorf("got (%q,%q,%q,%q), want (%q,%q,%q,%q)", an, ae, cn, ce, tc.authorName, tc.authorEmail, tc.comName, tc.comEmail)
			}
		})
	}
}

func TestMergeWithStoreScalarOverride(t *testing.T) {
	base := Sweatfile{Store: &Store{AuthorName: strPtr("Root"), AuthorEmail: strPtr("root@x")}}

	merged := base.MergeWith(Sweatfile{Store: &Store{AuthorName: strPtr("Repo")}})
	an, ae, _, _ := merged.StoreIdentity()
	if an != "Repo" || ae != "root@x" {
		t.Errorf("per-key override: got (%q,%q), want (Repo, root@x)", an, ae)
	}
	if an, _, _, _ := base.MergeWith(Sweatfile{}).StoreIdentity(); an != "Root" {
		t.Errorf("[store] not inherited: %q", an)
	}
	if *base.Store.AuthorName != "Root" {
		t.Errorf("receiver mutated by merge: %q", *base.Store.AuthorName)
	}
}
