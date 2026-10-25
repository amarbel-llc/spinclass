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

func TestMergeWithNeverCarriesStore(t *testing.T) {
	base := Sweatfile{Store: &Store{AuthorName: strPtr("Root")}}
	if merged := base.MergeWith(Sweatfile{Store: &Store{AuthorName: strPtr("Repo")}}); merged.Store != nil {
		t.Errorf("[store] leaked through MergeWith: %+v", merged.Store)
	}
	if merged := (Sweatfile{}).MergeWith(base); merged.Store != nil {
		t.Errorf("[store] leaked through MergeWith: %+v", merged.Store)
	}
	if *base.Store.AuthorName != "Root" {
		t.Errorf("receiver mutated by merge: %q", *base.Store.AuthorName)
	}
}
