package resolver

import (
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// The four tricky mods that CurseForge-mirrored jars broke on, verified
// against the live Modrinth API during development.
func TestSimilarityMatchesMirrorMods(t *testing.T) {
	cases := []struct {
		name   string
		cand   Candidate
		hit    modrinth.Hit
		minCon float64
	}{
		{
			name:   "InventoryProfilesNext mod name vs project title",
			cand:   Candidate{Meta: &modmeta.Meta{ModID: "inventoryprofilesnext", Name: "Inventory Profiles Next"}},
			hit:    modrinth.Hit{Slug: "inventory-profiles-next", Title: "Inventory Profiles Next"},
			minCon: confSlug,
		},
		{
			name:   "common-networking mod name vs Common Network project",
			cand:   Candidate{Meta: &modmeta.Meta{ModID: "commonnetworking", Name: "Common Network"}},
			hit:    modrinth.Hit{Slug: "common-network", Title: "Common Network"},
			minCon: confSlug,
		},
		{
			name:   "libIPN exact",
			cand:   Candidate{Meta: &modmeta.Meta{ModID: "libipn", Name: "libIPN"}},
			hit:    modrinth.Hit{Slug: "libipn", Title: "libIPN"},
			minCon: confSlug,
		},
		{
			name:   "YACL containment",
			cand:   Candidate{Meta: &modmeta.Meta{ModID: "yet_another_config_lib_v3", Name: "YetAnotherConfigLib"}},
			hit:    modrinth.Hit{Slug: "yacl", Title: "YetAnotherConfigLib (YACL)"},
			minCon: confNameHigh,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := similarity(c.cand, c.hit)
			if got < c.minCon {
				t.Errorf("similarity = %.2f, want >= %.2f", got, c.minCon)
			}
		})
	}
}

func TestSimilarityRejectsUnrelatedMods(t *testing.T) {
	cand := Candidate{Meta: &modmeta.Meta{ModID: "sodium", Name: "Sodium"}}
	for _, slug := range []string{"lithium", "iris", "zoomify", "starlight", "magnesium"} {
		if got := similarity(cand, modrinth.Hit{Slug: slug, Title: slug}); got > confNameLow {
			t.Errorf("similarity(sodium, %s) = %.2f, want <= %.2f", slug, got, confNameLow)
		}
	}
}

func TestSimilarityDistinguishesSodiumFromLithium(t *testing.T) {
	// Both are 7-letter names in the same ecosystem; only one is a match.
	sodium := Candidate{Meta: &modmeta.Meta{ModID: "sodium", Name: "Sodium"}}
	if got := similarity(sodium, modrinth.Hit{Slug: "sodium", Title: "Sodium"}); got < confSlug {
		t.Errorf("sodium vs sodium = %.2f, want >= %.2f", got, confSlug)
	}
	if got := similarity(sodium, modrinth.Hit{Slug: "lithium", Title: "Lithium"}); got >= confNameHigh {
		t.Errorf("sodium vs lithium = %.2f, should not be a confident match", got)
	}
}

func TestCandidateSlugs(t *testing.T) {
	c := Candidate{Meta: &modmeta.Meta{ModID: "inventoryprofilesnext", Name: "Inventory Profiles Next"}}
	got := candidateSlugs(c)
	want := map[string]bool{
		"inventory profiles next": false,
		"inventoryprofilesnext":   false,
		"inventory-profiles-next": false,
	}
	for _, s := range got {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, found := range want {
		if !found {
			t.Errorf("candidateSlugs missing %q (got %v)", s, got)
		}
	}
}

func TestCandidateSlugsYACL(t *testing.T) {
	c := Candidate{Meta: &modmeta.Meta{ModID: "yet_another_config_lib_v3", Name: "YetAnotherConfigLib"}}
	got := candidateSlugs(c)
	// The compact form should be present so we can probe "yetanotherconfiglib".
	var hasCompact bool
	for _, s := range got {
		if s == "yetanotherconfiglib" {
			hasCompact = true
		}
	}
	if !hasCompact {
		t.Errorf("expected compact slug in %v", got)
	}
}

func TestNamesCompatible(t *testing.T) {
	c := Candidate{Meta: &modmeta.Meta{ModID: "commonnetworking", Name: "Common Network"}}
	compat := &modrinth.Project{ID: "HIuqnQpi", Slug: "common-network", Title: "Common Network"}
	if !namesCompatible(c, compat) {
		t.Error("expected Common Network to be compatible")
	}
	bad := &modrinth.Project{ID: "x", Slug: "sodium", Title: "Sodium"}
	if namesCompatible(c, bad) {
		t.Error("did not expect sodium to be compatible with Common Network")
	}
}

func TestRankedHitPrefersBetterNameThenPopularity(t *testing.T) {
	c := Candidate{Meta: &modmeta.Meta{ModID: "sodium", Name: "Sodium"}}
	hits := []modrinth.Hit{
		{Slug: "sodium-legacy", Title: "Sodium Legacy", Downloads: 5_000_000},
		{Slug: "sodium", Title: "Sodium", Downloads: 10},
	}
	best := rankHits(c, hits)
	if best == nil {
		t.Fatal("expected a match")
	}
	if best.Hit.Slug != "sodium" {
		t.Errorf("chose %q, want the exact name match", best.Hit.Slug)
	}
}

func TestRankedHitReturnsNilWhenNothingMatches(t *testing.T) {
	c := Candidate{Meta: &modmeta.Meta{ModID: "moretools", Name: "More Tools"}}
	if got := rankHits(c, []modrinth.Hit{{Slug: "sodium", Title: "Sodium"}}); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestHyphenate(t *testing.T) {
	cases := map[string]string{
		"Inventory Profiles Next": "inventory-profiles-next",
		"YetAnotherConfigLib":     "yet-another-config-lib",
		"libIPN":                  "lib-ipn",
		"Sodium":                  "sodium",
	}
	for in, want := range cases {
		if got := hyphenate(in); got != want {
			t.Errorf("hyphenate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchQueries(t *testing.T) {
	c := Candidate{
		FileName: "common-networking-fabric-26.2-1.1.0.jar",
		Meta:     &modmeta.Meta{ModID: "commonnetworking", Name: "Common Network"},
	}
	got := searchQueries(c)
	if len(got) < 2 {
		t.Fatalf("expected several queries, got %v", got)
	}
	if got[0] != "Common Network" {
		t.Errorf("first query = %q, want the display name", got[0])
	}
}

func TestSearchQueriesFallsBackToFileName(t *testing.T) {
	c := Candidate{FileName: "tl_skin_cape_fabric_26.2-1.39.jar"}
	got := searchQueries(c)
	if len(got) == 0 {
		t.Fatal("expected a file name derived query")
	}
}
