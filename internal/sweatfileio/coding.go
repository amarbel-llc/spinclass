// Package sweatfileio holds the decode/encode/IO consumers of the generated
// sweatfile codec: Parse/Load/Save and the hierarchy loaders. These live in a
// package separate from internal/sweatfile so that the codegen package — the
// struct definitions plus tommy's generated *_tommy.go — contains no
// hand-written references to the generated Decode/Encode API. That keeps the
// codegen package type-checkable with the generated file absent, which is what
// `tommy generate` (go/packages Analyze) needs to regenerate across a breaking
// cst-API change. See tommy #93 and the dodder codegen-isolation pattern.
package sweatfileio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// Parse decodes sweatfile TOML bytes into a document. tommy's generated decoder
// already normalizes a present-but-empty array/map to a non-nil empty value
// while leaving an absent field nil — exactly the distinction
// sweatfile.MergeWith relies on (nil = inherit, empty = clear) — so no
// post-decode normalization is needed here.
func Parse(data []byte) (*sweatfile.SweatfileDocument, error) {
	return sweatfile.DecodeSweatfile(data)
}

// Load reads and parses the sweatfile at path. A missing file decodes as an
// empty document rather than an error.
func Load(path string) (*sweatfile.SweatfileDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sweatfile.DecodeSweatfile(nil)
		}
		return nil, err
	}
	return Parse(data)
}

// Save encodes doc and writes it to path, creating parent directories as
// needed. It is a free function (not a method) because doc's type is defined in
// the codegen package and methods can't be added from here.
func Save(doc *sweatfile.SweatfileDocument, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	output, err := doc.Encode()
	if err != nil {
		return err
	}
	return os.WriteFile(path, output, 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
