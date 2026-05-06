// Package resources implements the spinclass MCP resource providers.
//
// MadderProvider serves the resource_link URIs that
// `merge-this-session` and `check-this-session` emit when madder is
// build-time pinned (FDR 0005 follow-up). Each URI shape is
// `madder://blobs/<digest>` (matching `madder mcp serve`'s template) and
// resolves against the per-worktree blob store at
// `<worktree>/.madder/local/share/blob_stores/default/` established by
// FDR 0003.
package resources

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
)

const (
	uriScheme  = "madder"
	uriPrefix  = "madder://blobs/"
	uriHost    = "blobs"
	storeID    = ".default"
	mimeType   = "text/plain"
	tmplName   = "spinclass merge hook output"
	tmplDesc   = "Full stdout+stderr captured from a pre-merge hook run, content-addressed in the per-worktree madder store."
	tmplString = "madder://blobs/{digest}"
)

// MadderProvider implements server.ResourceProviderV1 by shelling out
// to the build-time-pinned madder binary against the worktree-scoped
// blob store. ListResources returns nothing — agents discover URIs via
// the resource_link content blocks emitted by merge/check tool
// responses, not by enumeration. The single ResourceTemplate
// describes the URI shape for clients that introspect the catalog.
type MadderProvider struct {
	worktreePath string
	binPath      string
}

// NewMadderProvider returns a provider rooted at worktreePath that
// invokes the madder binary at binPath. Both must be non-empty;
// callers are expected to gate construction on
// `embeds.MadderBin() != ""`.
func NewMadderProvider(worktreePath, binPath string) *MadderProvider {
	return &MadderProvider{worktreePath: worktreePath, binPath: binPath}
}

// ListResources returns an empty slice — see package doc.
func (p *MadderProvider) ListResources(_ context.Context) ([]protocol.Resource, error) {
	return []protocol.Resource{}, nil
}

// ListResourcesV1 mirrors ListResources for V1 clients.
func (p *MadderProvider) ListResourcesV1(_ context.Context, _ string) (*protocol.ResourcesListResultV1, error) {
	return &protocol.ResourcesListResultV1{Resources: []protocol.ResourceV1{}}, nil
}

// ListResourceTemplates returns the single template describing the
// `madder://blobs/{digest}` URI shape.
func (p *MadderProvider) ListResourceTemplates(_ context.Context) ([]protocol.ResourceTemplate, error) {
	return []protocol.ResourceTemplate{{
		URITemplate: tmplString,
		Name:        tmplName,
		Description: tmplDesc,
		MimeType:    mimeType,
	}}, nil
}

// ListResourceTemplatesV1 mirrors ListResourceTemplates for V1 clients.
func (p *MadderProvider) ListResourceTemplatesV1(_ context.Context, _ string) (*protocol.ResourceTemplatesListResultV1, error) {
	return &protocol.ResourceTemplatesListResultV1{
		ResourceTemplates: []protocol.ResourceTemplateV1{{
			URITemplate: tmplString,
			Name:        tmplName,
			Description: tmplDesc,
			MimeType:    mimeType,
		}},
	}, nil
}

// ReadResource shells out to `madder cat .default <digest>` with cwd
// scoped to the worktree and MADDER_CEILING_DIRECTORIES preventing
// store discovery from walking out of the worktree (mirrors
// internal/madder.Init/Write). The URI's host slot is a fixed `blobs`
// namespace marker matching `madder mcp serve`'s template; the local
// store name (`.default`) is supplied internally.
func (p *MadderProvider) ReadResource(ctx context.Context, uri string) (*protocol.ResourceReadResult, error) {
	digest, err := parseBlobURI(uri)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, p.binPath, "cat", storeID, digest)
	cmd.Dir = p.worktreePath
	cmd.Env = append(cmd.Environ(), "MADDER_CEILING_DIRECTORIES="+p.worktreePath)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return nil, fmt.Errorf("madder cat %s: %w\n%s", digest, err, stderr)
		}
		return nil, fmt.Errorf("madder cat %s: %w", digest, err)
	}

	return &protocol.ResourceReadResult{
		Contents: []protocol.ResourceContent{{
			URI:      uri,
			MimeType: mimeType,
			Text:     string(out),
		}},
	}, nil
}

// parseBlobURI accepts `madder://blobs/<digest>` and returns the digest.
// Anything else is rejected. The digest is required to be non-empty and
// free of slashes; madder itself enforces the digest shape on lookup.
func parseBlobURI(uri string) (string, error) {
	rest, ok := strings.CutPrefix(uri, uriPrefix)
	if !ok {
		return "", fmt.Errorf("unsupported resource URI %q: expected scheme %q with host %q", uri, uriScheme, uriHost)
	}
	if rest == "" {
		return "", fmt.Errorf("resource URI %q has empty digest", uri)
	}
	if strings.ContainsAny(rest, "/?#") {
		return "", fmt.Errorf("resource URI %q has invalid digest %q", uri, rest)
	}
	return rest, nil
}
