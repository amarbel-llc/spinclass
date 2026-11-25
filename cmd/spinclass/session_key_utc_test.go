package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// ssh-keygen -L prints the validity in the host's local zone with no zone
// letter. cert_expires must be the same instant in UTC whatever zone that is.
func TestCertExpiryIsReadInTheLocalZoneAndReportedInUTC(t *testing.T) {
	prev := time.Local
	t.Cleanup(func() { time.Local = prev })
	time.Local = time.FixedZone("UTC+2", 2*3600)

	got, err := parseCertExpiry("        Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00\n")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC) // 20:00 at UTC+2
	if !got.Equal(want) {
		t.Errorf("expiry instant = %v, want %v", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("expiry is in %v, want UTC", got.Location())
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(struct {
		E time.Time `json:"cert_expires"`
	}{got}); err != nil {
		t.Fatal(err)
	}
	if want := `{"cert_expires":"2026-11-26T18:00:00Z"}` + "\n"; buf.String() != want {
		t.Errorf("json = %q, want %q", buf.String(), want)
	}
}
