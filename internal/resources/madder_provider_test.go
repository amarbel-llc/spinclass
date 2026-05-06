package resources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBlobURI(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		got, err := parseBlobURI("madder://blobs/sha256-deadbeef")
		if err != nil {
			t.Fatalf("parseBlobURI: %v", err)
		}
		if got != "sha256-deadbeef" {
			t.Errorf("got digest %q, want sha256-deadbeef", got)
		}
	})

	cases := []struct {
		name string
		uri  string
	}{
		{"wrong scheme", "spinclass://blobs/sha256-x"},
		{"missing host", "madder://sha256-x"},
		{"wrong host", "madder://.default/sha256-x"},
		{"empty digest", "madder://blobs/"},
		{"slash in digest", "madder://blobs/sha256/extra"},
		{"query in digest", "madder://blobs/sha256-x?foo=bar"},
		{"fragment in digest", "madder://blobs/sha256-x#frag"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseBlobURI(tc.uri); err == nil {
				t.Fatalf("expected error parsing %q, got nil", tc.uri)
			}
		})
	}
}

// fakeMadderBin writes a shell script that, when invoked as
// `<bin> cat .default <digest>`, prints "BLOB:<digest>" to stdout
// and exits 0. Anything else (including the wrong store argument or a
// missing digest) exits non-zero with a recognizable stderr message.
// The script also records its own arg/env/cwd into a sidecar file so
// the test can assert on what spinclass invoked.
func fakeMadderBin(t *testing.T) (binPath, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-madder")
	recordPath = filepath.Join(dir, "record")
	script := `#!/bin/sh
{
  echo "args: $*"
  echo "cwd: $PWD"
  echo "ceiling: ${MADDER_CEILING_DIRECTORIES}"
} >"` + recordPath + `"
if [ "$1" != "cat" ] || [ "$2" != ".default" ] || [ -z "$3" ]; then
  echo "fake-madder: bad invocation: $*" >&2
  exit 2
fi
printf 'BLOB:%s' "$3"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake madder: %v", err)
	}
	return binPath, recordPath
}

func TestReadResource_Success(t *testing.T) {
	wt := t.TempDir()
	bin, record := fakeMadderBin(t)

	provider := NewMadderProvider(wt, bin)
	res, err := provider.ReadResource(context.Background(), "madder://blobs/sha256-cafebabe")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != "madder://blobs/sha256-cafebabe" {
		t.Errorf("URI = %q, want madder://blobs/sha256-cafebabe", c.URI)
	}
	if c.MimeType != "text/plain" {
		t.Errorf("MimeType = %q, want text/plain", c.MimeType)
	}
	if c.Text != "BLOB:sha256-cafebabe" {
		t.Errorf("Text = %q, want BLOB:sha256-cafebabe", c.Text)
	}

	rec, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("reading record: %v", err)
	}
	got := string(rec)
	for _, want := range []string{
		"args: cat .default sha256-cafebabe",
		"cwd: " + wt,
		"ceiling: " + wt,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in record, got:\n%s", want, got)
		}
	}
}

func TestReadResource_MadderFails(t *testing.T) {
	wt := t.TempDir()
	bin, _ := fakeMadderBin(t)

	provider := NewMadderProvider(wt, bin)
	// Invalid digest passes URI parsing but is impossible — but our fake
	// rejects empty $3 with exit 2 + stderr. To trigger that path here
	// we craft a URI the parser accepts then tweak the fake; simpler:
	// pass a valid URI whose digest our fake rejects. Switch the fake
	// rejection trigger by giving an unknown subcommand instead.
	provider.binPath = "/nonexistent/madder-binary-that-does-not-exist"

	if _, err := provider.ReadResource(context.Background(), "madder://blobs/sha256-x"); err == nil {
		t.Fatal("expected error when madder binary is missing")
	}
}

func TestReadResource_RejectsBadURI(t *testing.T) {
	wt := t.TempDir()
	bin, _ := fakeMadderBin(t)
	provider := NewMadderProvider(wt, bin)

	if _, err := provider.ReadResource(context.Background(), "spinclass://merge-output/sha256-x"); err == nil {
		t.Fatal("expected URI parse error for unsupported scheme")
	}
}

func TestListResourcesEmpty(t *testing.T) {
	provider := NewMadderProvider("/tmp", "/usr/bin/false")
	got, err := provider.ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 resources, got %d", len(got))
	}

	gotV1, err := provider.ListResourcesV1(context.Background(), "")
	if err != nil {
		t.Fatalf("ListResourcesV1: %v", err)
	}
	if len(gotV1.Resources) != 0 {
		t.Errorf("expected 0 V1 resources, got %d", len(gotV1.Resources))
	}
}

func TestListResourceTemplates(t *testing.T) {
	provider := NewMadderProvider("/tmp", "/usr/bin/false")
	got, err := provider.ListResourceTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 template, got %d", len(got))
	}
	if got[0].URITemplate != "madder://blobs/{digest}" {
		t.Errorf("template URITemplate = %q, want madder://blobs/{digest}", got[0].URITemplate)
	}

	gotV1, err := provider.ListResourceTemplatesV1(context.Background(), "")
	if err != nil {
		t.Fatalf("ListResourceTemplatesV1: %v", err)
	}
	if len(gotV1.ResourceTemplates) != 1 {
		t.Fatalf("expected 1 V1 template, got %d", len(gotV1.ResourceTemplates))
	}
}
