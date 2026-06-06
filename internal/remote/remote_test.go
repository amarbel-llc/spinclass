package remote

import (
	"reflect"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantHost string
		wantID   string
		wantOK   bool
	}{
		{
			name:     "host-prefixed target",
			target:   "devbox:crisp-catalpa",
			wantHost: "devbox",
			wantID:   "crisp-catalpa",
			wantOK:   true,
		},
		{
			name:   "plain id resolves locally",
			target: "crisp-catalpa",
		},
		{
			name:   "prefix may not contain slash",
			target: "a/b:c",
		},
		{
			name:   "empty prefix",
			target: ":x",
		},
		{
			name:   "empty id",
			target: "x:",
		},
		{
			name:   "empty target",
			target: "",
		},
		{
			name:     "id may contain further colons",
			target:   "devbox:a:b",
			wantHost: "devbox",
			wantID:   "a:b",
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, id, ok := ParseTarget(tt.target)
			if host != tt.wantHost || id != tt.wantID || ok != tt.wantOK {
				t.Errorf("ParseTarget(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.target, host, id, ok, tt.wantHost, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestAttachArgv(t *testing.T) {
	tests := []struct {
		name string
		r    sweatfile.Remote
		id   string
		want []string
	}{
		{
			name: "default template uses Dest()=Name",
			r:    sweatfile.Remote{Name: "devbox"},
			id:   "crisp-catalpa",
			want: []string{"ssh", "-t", "devbox", "spinclass", "resume", "crisp-catalpa"},
		},
		{
			name: "default template uses explicit ssh destination",
			r:    sweatfile.Remote{Name: "devbox", SSH: "sasha@devbox.lan"},
			id:   "crisp-catalpa",
			want: []string{"ssh", "-t", "sasha@devbox.lan", "spinclass", "resume", "crisp-catalpa"},
		},
		{
			name: "explicit template substitutes every element",
			r: sweatfile.Remote{
				Name:   "devbox",
				SSH:    "sasha@devbox.lan",
				Attach: []string{"mosh", "{ssh}", "--", "sc", "resume", "{id}-on-{ssh}"},
			},
			id:   "crisp-catalpa",
			want: []string{"mosh", "sasha@devbox.lan", "--", "sc", "resume", "crisp-catalpa-on-sasha@devbox.lan"},
		},
		{
			name: "substitution is literal, no shell",
			r: sweatfile.Remote{
				Name:   "devbox",
				Attach: []string{"ssh", "{ssh}", "spinclass resume {id}"},
			},
			id:   "$(rm -rf /); id",
			want: []string{"ssh", "devbox", "spinclass resume $(rm -rf /); id"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AttachArgv(tt.r, tt.id)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AttachArgv(%+v, %q) = %v, want %v", tt.r, tt.id, got, tt.want)
			}
		})
	}
}
