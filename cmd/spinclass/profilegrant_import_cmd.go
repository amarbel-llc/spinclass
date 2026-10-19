package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/grantstore"
	"code.linenisgreat.com/spinclass/internal/grantverify"
	"code.linenisgreat.com/spinclass/internal/profilegrant"
)

// Seams for command tests: the store location, the signers file, the clock and
// the standard streams.
var (
	profileGrantImportStoreDir = grantstore.DefaultDir
	profileGrantImportSigners  = grantverify.DefaultSignersPath
	profileGrantImportNow      = time.Now
	profileGrantImportStdin    = func() io.Reader { return os.Stdin }
	profileGrantImportOut      = func() io.Writer { return os.Stdout }
)

// maxImportBytes bounds what the command reads; a record is well under 1 KiB.
const maxImportBytes = 1 << 20

// registerProfileGrantImportCommand registers the import command. Like bless it
// sets RunCLI and no Run, so it is never an MCP tool: importing is how a
// grant becomes enforceable, and an agent must not be able to do it.
func registerProfileGrantImportCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "profile-grant-import",
		Title: "Import a card-signed profile grant",
		Description: command.Description{
			Short: "Verify card-signed profile grant records and store them for spawn enforcement (FDR 0034)",
			Long: "Reads the record JSON produced by profile-grant-bless from a file, or stdin when the file is \"-\", " +
				"and also accepts a JSON array so a widening and the record it widens can be imported together. " +
				"Every record is verified against the profile-grant signers file (there is no way to skip this) " +
				"before anything is stored; a widening verifies only when the record it widens is in the same batch or already stored. " +
				"Only card-signed root records can be imported: narrowing links are minted by spawn. " +
				"Each record that no other record in the batch widens becomes its grantee's current record, " +
				"but only after every record in the batch is stored. A grantee that already has a different current record " +
				"is not silently rebound: the import is refused unless the new record widens the current one or --replace is passed.",
		},
		Params: []command.Param{
			{Name: "file", Type: command.String, Description: "Record JSON file, or \"-\" for stdin", Required: true},
			{Name: "replace", Type: command.Bool, Description: "Rebind a grantee that already has a different current record (default: refuse)"},
		},
		RunCLI: runProfileGrantImport,
	})
}

func runProfileGrantImport(_ context.Context, args json.RawMessage) error {
	var f struct {
		File    string `json:"file"`
		Replace bool   `json:"replace"`
	}
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("profile-grant-import: %w", err)
	}
	if f.File == "" {
		return errors.New("profile-grant-import: a record file (or -) is required")
	}
	var src io.Reader
	if f.File == "-" {
		src = profileGrantImportStdin()
	} else {
		fh, err := os.Open(f.File)
		if err != nil {
			return fmt.Errorf("profile-grant-import: %w", err)
		}
		defer fh.Close()
		src = fh
	}
	data, err := io.ReadAll(io.LimitReader(src, maxImportBytes+1))
	if err != nil {
		return fmt.Errorf("profile-grant-import: %w", err)
	}
	if len(data) > maxImportBytes {
		return fmt.Errorf("profile-grant-import: input is larger than %d bytes", maxImportBytes)
	}
	if err := importGrantRecords(data, profileGrantImportNow(), profileGrantImportOut(), f.Replace); err != nil {
		return fmt.Errorf("profile-grant-import: %w", err)
	}
	return nil
}

type importCandidate struct {
	rec   profilegrant.Record
	grant profilegrant.Grant
}

func decodeImportRecords(data []byte) ([]profilegrant.Record, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("no record in the input")
	}
	if data[0] != '[' {
		var rec profilegrant.Record
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("record JSON: %w", err)
		}
		return []profilegrant.Record{rec}, nil
	}
	var recs []profilegrant.Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("record JSON array: %w", err)
	}
	if len(recs) == 0 {
		return nil, errors.New("empty record array")
	}
	return recs, nil
}

// importGrantRecords verifies every record before storing any, so a bad record
// in a batch leaves the store as it was.
//
// Order: verify all, Put all, then Bind. A grantee that already has a different
// current record is not rebound unless replace is set (a record widening the
// current one is the sanctioned supersession and needs no flag).
func importGrantRecords(data []byte, now time.Time, out io.Writer, replace bool) error {
	recs, err := decodeImportRecords(data)
	if err != nil {
		return err
	}
	batch, err := parseImportBatch(recs)
	if err != nil {
		return err
	}
	ordered, err := orderWidenedFirst(batch)
	if err != nil {
		return err
	}

	// Fail closed: without the card's keys nothing can be trusted.
	keys, err := grantverify.LoadSigners(profileGrantImportSigners())
	if err != nil {
		return err
	}
	dir, err := profileGrantImportStoreDir()
	if err != nil {
		return err
	}
	store, err := grantstore.Open(dir)
	if err != nil {
		return err
	}

	inBatch := map[string]profilegrant.Record{}
	for _, c := range ordered {
		inBatch[c.rec.Digest] = c.rec
	}
	resolve := func(d string) (profilegrant.Record, bool) {
		if r, ok := inBatch[d]; ok {
			return r, true
		}
		return store.Get(d)
	}
	verifier := profilegrant.NewVerifier(grantverify.NewRootVerifier(keys), profilegrant.LocalLinkVerifier{})
	for _, c := range ordered {
		if _, err := profilegrant.Walk(now, c.rec, resolve, verifier, profilegrant.WalkOptions{}); err != nil {
			return fmt.Errorf("not importing: %w", err)
		}
	}

	widenedInBatch := map[string]bool{}
	for _, c := range ordered {
		if _, ok := inBatch[c.grant.Widens]; ok {
			widenedInBatch[c.grant.Widens] = true
		}
	}
	var toBind []importCandidate
	for _, c := range ordered {
		if !widenedInBatch[c.rec.Digest] {
			toBind = append(toBind, c)
		}
	}

	// Refuse before writing anything if a binding would be silently replaced.
	if !replace {
		for _, c := range toBind {
			cur, ok := store.Current(c.grant.Grantee)
			if !ok || cur.Digest == c.rec.Digest || c.grant.Widens == cur.Digest {
				continue
			}
			return fmt.Errorf("not importing: grantee %q is already bound to record %s, which %s does not widen; pass --replace to rebind it",
				c.grant.Grantee, cur.Digest, c.rec.Digest)
		}
	}

	// Store every record first; bind only once all of them are stored.
	for _, c := range ordered {
		if err := store.Put(c.rec); err != nil {
			return err
		}
	}
	bound := map[string]bool{}
	for _, c := range toBind {
		if err := store.Bind(c.grant.Grantee, c.rec.Digest); err != nil {
			return err
		}
		bound[c.rec.Digest] = true
	}
	for _, c := range ordered {
		printImported(out, c, bound[c.rec.Digest])
	}
	return nil
}

func parseImportBatch(recs []profilegrant.Record) ([]importCandidate, error) {
	seen := map[string]bool{}
	var batch []importCandidate
	for _, rec := range recs {
		if !rec.Root {
			return nil, fmt.Errorf("record %s is not a root record: narrowing links are minted by spawn and cannot be imported", rec.Digest)
		}
		g, err := profilegrant.ParseCanonicalText(rec.Text)
		if err != nil {
			return nil, fmt.Errorf("record %s: %w", rec.Digest, err)
		}
		if seen[rec.Digest] {
			continue
		}
		seen[rec.Digest] = true
		batch = append(batch, importCandidate{rec: rec, grant: g})
	}
	return batch, nil
}

// orderWidenedFirst puts each record after the record it widens, when that one
// is in the batch, so the widened record is stored first.
func orderWidenedFirst(batch []importCandidate) ([]importCandidate, error) {
	digests := map[string]bool{}
	for _, c := range batch {
		digests[c.rec.Digest] = true
	}
	placed := map[string]bool{}
	var ordered []importCandidate
	for len(ordered) < len(batch) {
		progress := false
		for _, c := range batch {
			if placed[c.rec.Digest] {
				continue
			}
			if w := c.grant.Widens; digests[w] && !placed[w] {
				continue
			}
			placed[c.rec.Digest] = true
			ordered = append(ordered, c)
			progress = true
		}
		if !progress {
			return nil, errors.New("records widen each other in a cycle")
		}
	}
	return ordered, nil
}

func printImported(out io.Writer, c importCandidate, bound bool) {
	g := c.grant
	names := make([]string, 0, len(g.Profiles))
	for n := range g.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	pinned := make([]string, len(names))
	for i, n := range names {
		pinned[i] = n + "=" + g.Profiles[n]
	}
	fmt.Fprintf(out, "imported %s\n", c.rec.Digest)
	fmt.Fprintf(out, "  grantee: %s\n", g.Grantee)
	fmt.Fprintf(out, "  session: %s\n", g.Session)
	fmt.Fprintf(out, "  profiles: %s\n", strings.Join(pinned, " "))
	fmt.Fprintf(out, "  onward: %s\n", g.Onward)
	fmt.Fprintf(out, "  expires: %s\n", g.Expires.UTC().Format(time.RFC3339))
	fmt.Fprintf(out, "  current for grantee: %t\n", bound)
}
