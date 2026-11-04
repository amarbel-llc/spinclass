package spawn

import (
	"encoding/hex"
	"reflect"
	"testing"

	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// The resolver carries the profile's moxy table, and its base's, as clown
// reports them (clown#343); a profile without one has nil.
func TestClownResolverMapsTheMoxyTable(t *testing.T) {
	res := resolvedWith(profilelookup.Selection{Selective: true, Kept: append(profilelookup.RequiredServers(), moxyServer, justUsServer)})
	res.Moxy = &profilelookup.MoxyTable{
		Placement:  "in-unit",
		AllowTools: []string{"mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__rg_search"},
		Tools:      []string{"mcp__plugin_moxy_moxy__rg_search"},
	}
	baseDigest := make([]byte, 32)
	baseDigest[0] = 0xff
	res.Base = &profilelookup.Resolved{
		Name: "base-b", Source: profilelookup.SourceBuiltin, DigestSHA256: hex.EncodeToString(baseDigest),
		Moxy: &profilelookup.MoxyTable{Placement: "in-unit", AllowTools: []string{"mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__rg_search"}, Tools: []string{"mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__rg_search"}},
	}

	got, err := clownResolver{lookup: fakeLookup(res, nil)}.Resolve("cheap")
	if err != nil {
		t.Fatal(err)
	}
	want := &MoxyTable{Placement: "in-unit", AllowTools: res.Moxy.AllowTools, Tools: res.Moxy.Tools}
	if !reflect.DeepEqual(got.Moxy, want) {
		t.Errorf("Moxy = %+v, want %+v", got.Moxy, want)
	}
	if got.BaseProfile == nil || got.BaseProfile.Moxy == nil || got.BaseProfile.Moxy.Placement != "in-unit" || len(got.BaseProfile.Moxy.Tools) != 2 {
		t.Errorf("BaseProfile.Moxy = %+v, want the base's table", got.BaseProfile)
	}

	plain, err := clownResolver{lookup: fakeLookup(resolvedWith(profilelookup.Selection{}), nil)}.Resolve("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Moxy != nil {
		t.Errorf("a profile without a table mapped to %+v, want nil", plain.Moxy)
	}
}
