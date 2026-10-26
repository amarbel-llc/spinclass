package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
)

// #378: two unrelated records for one grantee in a batch would both become its
// current record, the last one silently winning. Refused, --replace or not,
// before anything is stored.
func TestImportRefusesTwoUnrelatedRecordsForOneGrantee(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	first := blessForImport(t, card, nil)
	second := blessForImport(t, card, func(in *blessInput) { in.Nonce = "fedcba9876543210fedcba9876543210" })
	file := writeRecordsFile(t, []profilegrant.Record{first, second})

	requireErrContaining(t, runImport(t, file), `grantee "swift-elder"`)
	requireNothingStored(t, env, first, second)
	requireErrContaining(t, runImportReplace(t, file), `grantee "swift-elder"`)
	requireNothingStored(t, env, first, second)
}

// squatBinding makes Bind for grantee fail: a non-empty directory where its
// index file goes cannot be renamed over.
func squatBinding(t *testing.T, env *importEnv, grantee string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(env.dir, "index", grantee, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// #378: a Bind failing partway through rolls back the bindings made before it,
// restoring a grantee's previous record.
func TestImportRollsBackEarlierBindingsWhenABindFails(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	prior := blessForImport(t, card, nil)
	if err := runImport(t, writeRecordsFile(t, prior)); err != nil {
		t.Fatal(err)
	}
	replacement := blessForImport(t, card, func(in *blessInput) { in.Nonce = "fedcba9876543210fedcba9876543210" })
	other := blessForImport(t, card, func(in *blessInput) {
		in.Grantee = "other-one"
		in.Nonce = "00000000000000000000000000000002"
	})
	squatBinding(t, env, "other-one")

	if err := runImportReplace(t, writeRecordsFile(t, []profilegrant.Record{replacement, other})); err == nil {
		t.Fatal("import succeeded despite a failing Bind")
	}
	if cur, ok := env.store(t).Current("swift-elder"); !ok || cur.Digest != prior.Digest {
		t.Fatalf("swift-elder is bound to %v, want its previous record %s restored", cur.Digest, prior.Digest)
	}
}

// A grantee that had no binding before is left unbound by the rollback.
func TestImportRollbackUnbindsANewlyBoundGrantee(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	good := blessForImport(t, card, nil)
	other := blessForImport(t, card, func(in *blessInput) {
		in.Grantee = "other-one"
		in.Nonce = "00000000000000000000000000000002"
	})
	squatBinding(t, env, "other-one")

	if err := runImport(t, writeRecordsFile(t, []profilegrant.Record{good, other})); err == nil {
		t.Fatal("import succeeded despite a failing Bind")
	}
	if _, ok := env.store(t).Current("swift-elder"); ok {
		t.Fatal("swift-elder stayed bound after the batch failed")
	}
}
