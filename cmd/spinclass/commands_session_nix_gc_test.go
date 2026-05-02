package main

import (
	"strings"
	"testing"
)

func TestParseNixGCFlag(t *testing.T) {
	cases := []struct {
		in        string
		wantNil   bool
		wantValue bool
		wantErr   string
	}{
		{in: "", wantNil: true},
		{in: "true", wantValue: true},
		{in: "false", wantValue: false},
		{in: "True", wantErr: "must be 'true' or 'false'"},
		{in: "1", wantErr: "must be 'true' or 'false'"},
		{in: "garbage", wantErr: "must be 'true' or 'false'"},
	}
	for _, c := range cases {
		t.Run("in="+c.in, func(t *testing.T) {
			got, err := parseNixGCFlag(c.in)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil with %v", c.wantErr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantNil {
				if got != nil {
					t.Fatalf("got = %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want pointer to %v", c.wantValue)
			}
			if *got != c.wantValue {
				t.Errorf("*got = %v, want %v", *got, c.wantValue)
			}
		})
	}
}
