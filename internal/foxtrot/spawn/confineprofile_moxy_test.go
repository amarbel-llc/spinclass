package spawn

import (
	"slices"
	"strings"
	"testing"
)

// R3' (spinclass#457): a confined spawn may keep moxy only through a moxy table
// (clown profile protocol 3) that places it in the unit, names tools under its
// own ceiling, and names nothing outside the ratified default.

const (
	toolRgSearch   = "mcp__plugin_moxy_moxy__rg_search"
	toolFolioRead  = "mcp__plugin_moxy_moxy__folio_read"
	toolFolioCp    = "mcp__plugin_moxy_moxy__folio_cp"
	toolEnvClock   = "mcp__plugin_moxy_moxy__env_clock"
	toolManToc     = "mcp__plugin_moxy_moxy__man_toc"
	toolToolbox    = "mcp__plugin_moxy_moxy__toolbox_sha256"
	toolGritCommit = "mcp__plugin_moxy_moxy__grit_commit"
	toolSmithIssue = "mcp__plugin_moxy_moxy__smith_issue-create"
	toolRestart    = "mcp__plugin_moxy_moxy__restart"
	toolAsync      = "mcp__plugin_moxy_moxy__async"
	toolChixBuild  = "mcp__plugin_moxy_moxy__chix_build"
)

// inUnitProfile keeps moxy and just-us with a table placing moxy in-unit;
// allow is the ceiling, tools the effective set (allow when nil).
func inUnitProfile(allow, tools []string) ResolvedProfile {
	if tools == nil {
		tools = allow
	}
	return ResolvedProfile{
		Name: "locked", Digest: "digest-locked", Source: "builtin",
		KeptServers: append(slices.Clone(requiredKept), moxyServer, justUsServer),
		Moxy:        &MoxyTable{Placement: moxyPlacementInUnit, AllowTools: allow, Tools: tools},
	}
}

func TestCheckConfinedProfileInUnitMoxy(t *testing.T) {
	defaults := []string{toolRgSearch, toolFolioRead, toolEnvClock, toolManToc, toolToolbox}
	withPlacement := func(p string) ResolvedProfile {
		rp := inUnitProfile(defaults, nil)
		rp.Moxy.Placement = p
		return rp
	}
	cases := []struct {
		name    string
		rp      ResolvedProfile
		wantErr []string // substrings; nil means accepted
	}{
		{name: "in-unit, ratified tools under the ceiling", rp: inUnitProfile(defaults, nil)},
		{name: "in-unit, a narrower effective set than the ceiling", rp: inUnitProfile(defaults, []string{toolRgSearch})},
		{name: "moxy kept with no table is still refused", rp: ResolvedProfile{Name: "locked", Source: "builtin", KeptServers: append(slices.Clone(requiredKept), moxyServer, justUsServer)}, wantErr: []string{"no moxy table", "in-unit"}},
		{name: "frontend placement", rp: withPlacement(moxyPlacementFrontend), wantErr: []string{"in the frontend", "grit_*"}},
		{name: "twin placement", rp: withPlacement(moxyPlacementTwin), wantErr: []string{"twin", "not specified"}},
		{name: "unknown placement", rp: withPlacement("sidecar"), wantErr: []string{`"sidecar"`, "does not know"}},
		{name: "no tools", rp: inUnitProfile(defaults, []string{}), wantErr: []string{"names no tools"}},
		{name: "a tool outside its own ceiling", rp: inUnitProfile([]string{toolRgSearch}, []string{toolRgSearch, toolFolioRead}), wantErr: []string{toolFolioRead, "outside its own ceiling"}},
		{name: "grit", rp: inUnitProfile([]string{toolGritCommit}, nil), wantErr: []string{toolGritCommit, "moxy's own git"}},
		{name: "smith", rp: inUnitProfile([]string{toolSmithIssue}, nil), wantErr: []string{toolSmithIssue, "forge-writing"}},
		{name: "restart", rp: inUnitProfile([]string{toolRestart}, nil), wantErr: []string{toolRestart, "moxy's own tools"}},
		{name: "async", rp: inUnitProfile([]string{toolAsync}, nil), wantErr: []string{toolAsync, "moxy's own tools"}},
		{name: "folio cp is outside the ratified subset", rp: inUnitProfile([]string{toolFolioCp}, nil), wantErr: []string{toolFolioCp, "outside the ratified subset", "file-type"}},
		{name: "a moxin outside the default", rp: inUnitProfile([]string{toolChixBuild}, nil), wantErr: []string{toolChixBuild, `"chix"`, "not in the ratified default"}},
		{name: "not an mcp name", rp: inUnitProfile([]string{"rg.search"}, nil), wantErr: []string{"rg.search", "mcp__<server>__<tool>"}},
		{name: "in-unit moxy but just-us dropped", rp: func() ResolvedProfile {
			rp := inUnitProfile(defaults, nil)
			rp.KeptServers = append(slices.Clone(requiredKept), moxyServer)
			return rp
		}(), wantErr: []string{"drops the " + `"just-us/just-us"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withResolver(t, &fakeResolver{})
			_, err := checkConfinedProfile("locked", c.rp)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("checkConfinedProfile = %v, want accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatal("checkConfinedProfile = nil, want a refusal")
			}
			for _, want := range c.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not contain %q", err, want)
				}
			}
		})
	}
}

// The moxy table is compared narrow-only against the base: a child may drop
// tools, never add one, change the placement or widen the ceiling.
func TestCatalogWideningsMoxyTable(t *testing.T) {
	table := func(placement string, allow, tools []string) *MoxyTable {
		return &MoxyTable{Placement: placement, AllowTools: allow, Tools: tools}
	}
	with := func(m *MoxyTable) ResolvedProfile {
		rp := narrowableBase()
		rp.Moxy = m
		return rp
	}
	base := with(table(moxyPlacementInUnit, []string{toolRgSearch, toolFolioRead}, []string{toolRgSearch, toolFolioRead}))
	cases := []struct {
		name  string
		child ResolvedProfile
		want  []string
	}{
		{name: "same table", child: with(table(moxyPlacementInUnit, []string{toolRgSearch, toolFolioRead}, []string{toolRgSearch, toolFolioRead}))},
		{name: "drops a tool", child: with(table(moxyPlacementInUnit, []string{toolRgSearch, toolFolioRead}, []string{toolRgSearch}))},
		{name: "adds a tool", child: with(table(moxyPlacementInUnit, []string{toolRgSearch, toolFolioRead, toolEnvClock}, []string{toolRgSearch, toolEnvClock})), want: []string{"widens the moxy ceiling by " + toolEnvClock, "adds moxy tool " + toolEnvClock}},
		{name: "changes placement", child: with(table(moxyPlacementFrontend, []string{toolRgSearch}, []string{toolRgSearch})), want: []string{"changes moxy placement in-unit to frontend"}},
		{name: "drops the table", child: with(nil), want: []string{"drops the moxy table (base places moxy in-unit)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := catalogWidenings(base, c.child)
			if !slices.Equal(got, c.want) {
				t.Errorf("catalogWidenings = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("adds a table the base lacks", func(t *testing.T) {
		got := catalogWidenings(narrowableBase(), with(table(moxyPlacementInUnit, []string{toolRgSearch}, []string{toolRgSearch})))
		if want := []string{"adds a moxy table (placement in-unit; base has none)"}; !slices.Equal(got, want) {
			t.Errorf("catalogWidenings = %q, want %q", got, want)
		}
	})
}
