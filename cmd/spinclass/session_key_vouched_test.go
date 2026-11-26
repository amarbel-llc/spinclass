package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A-H1, stated as a limit: the fingerprint record the printed key is checked
// against is a file beside the holder's socket, which any unconfined session of
// the account could have replaced. The command says so itself, in text mode on
// stderr and in --json as a field, and stdout stays exactly the two lines.
func TestSessionKeyTextModeStatesWhoCouldHaveWrittenTheRecordOnStderr(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if got, want := e.out.String(), khPub+"\nSHA256:FAKEFP\n"; got != want {
		t.Errorf("stdout %q, want exactly %q", got, want)
	}
	for _, want := range []string{"vouched only against confined sessions", "an unconfined session of this account could have written this record"} {
		if !strings.Contains(e.errOut.String(), want) {
			t.Errorf("stderr %q does not say %q", e.errOut.String(), want)
		}
	}
}

func TestSessionKeyJSONCarriesTheVouchedLimitAsAField(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	m := e.runJSON(t, "a")
	v, ok := m["vouched"].(string)
	if !ok || v != sessionKeyVouched {
		t.Errorf("vouched = %v, want the sentence %q", m["vouched"], sessionKeyVouched)
	}
	if !strings.HasPrefix(v, "vouched only against confined sessions") {
		t.Errorf("vouched %q must open by naming what it covers", v)
	}
	// JSON mode keeps stderr for warnings about the certificate: the limit is in the document.
	if strings.Contains(e.errOut.String(), "vouched") {
		t.Errorf("json mode also printed the limit on stderr: %q", e.errOut.String())
	}
}
