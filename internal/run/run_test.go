package run

import (
	"slices"
	"strings"
	"testing"
)

func TestParseArgsUtilForm(t *testing.T) {
	spec, err := ParseArgs([]string{"--description", "bump", "--no-close", "--", "nix", "flake", "update"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if spec.Description != "bump" {
		t.Errorf("Description = %q, want bump", spec.Description)
	}
	if !spec.NoClose || spec.NoMerge || spec.LocalOnly {
		t.Errorf("flags = %+v, want only NoClose", spec)
	}
	if !slices.Equal(spec.Util, []string{"nix", "flake", "update"}) {
		t.Errorf("Util = %v", spec.Util)
	}
	if spec.Script != nil {
		t.Errorf("Script should be nil for the util form, got %q", spec.Script)
	}
}

func TestParseArgsAllFlags(t *testing.T) {
	spec, err := ParseArgs([]string{"--no-merge", "--no-close", "--local-only", "--format=ndjson", "--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !spec.NoMerge || !spec.NoClose || !spec.LocalOnly {
		t.Errorf("flags = %+v, want all three set", spec)
	}
	if spec.Format != "ndjson" {
		t.Errorf("Format = %q, want ndjson", spec.Format)
	}
}

func TestParseArgsStdinForm(t *testing.T) {
	spec, err := ParseArgs([]string{"--description", "x"}, strings.NewReader("echo hi\n"))
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if len(spec.Util) != 0 {
		t.Errorf("Util should be empty for stdin form, got %v", spec.Util)
	}
	if string(spec.Script) != "echo hi\n" {
		t.Errorf("Script = %q", spec.Script)
	}
}

func TestParseArgsEmptyIsError(t *testing.T) {
	if _, err := ParseArgs(nil, strings.NewReader("   \n\t")); err == nil {
		t.Error("blank stdin and no util: want error, got nil")
	}
	if _, err := ParseArgs(nil, nil); err == nil {
		t.Error("no stdin and no util: want error, got nil")
	}
}

func TestParseArgsUtilWinsOverStdin(t *testing.T) {
	// A `--` util takes the util form; stdin is not consumed.
	spec, err := ParseArgs([]string{"--", "echo", "x"}, strings.NewReader("ignored\n"))
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !slices.Equal(spec.Util, []string{"echo", "x"}) {
		t.Errorf("Util = %v", spec.Util)
	}
	if spec.Script != nil {
		t.Errorf("Script should be nil when `--` present, got %q", spec.Script)
	}
}

func TestParseArgsUnknownFlag(t *testing.T) {
	if _, err := ParseArgs([]string{"--bogus", "--", "true"}, nil); err == nil {
		t.Error("unknown flag: want error, got nil")
	}
}

func TestParseArgsDanglingValueFlag(t *testing.T) {
	if _, err := ParseArgs([]string{"--description"}, nil); err == nil {
		t.Error("--description with no value: want error, got nil")
	}
}

func TestSplitShebang(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantInterp []string
		wantBody   string
	}{
		{
			name:       "no shebang defaults to sh",
			script:     "echo hi\n",
			wantInterp: []string{"sh"},
			wantBody:   "echo hi\n",
		},
		{
			name:       "bash shebang",
			script:     "#!/usr/bin/env bash\nset -e\necho hi\n",
			wantInterp: []string{"/usr/bin/env", "bash"},
			wantBody:   "#!/usr/bin/env bash\nset -e\necho hi\n",
		},
		{
			name:       "plain bin sh shebang",
			script:     "#!/bin/sh\necho hi\n",
			wantInterp: []string{"/bin/sh"},
			wantBody:   "#!/bin/sh\necho hi\n",
		},
		{
			name:       "empty shebang falls back to sh",
			script:     "#!\necho hi\n",
			wantInterp: []string{"sh"},
			wantBody:   "#!\necho hi\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interp, body := splitShebang([]byte(tt.script))
			if !slices.Equal(interp, tt.wantInterp) {
				t.Errorf("interp = %v, want %v", interp, tt.wantInterp)
			}
			if string(body) != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}
