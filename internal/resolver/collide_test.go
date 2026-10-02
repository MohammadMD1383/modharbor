package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// The real-world collision that motivated every corroboration check: a user's
// private mod named "More Tools" version 1.0.0 must never be resolved to an
// unrelated published project that happens to share the name and version
// string. Migrating on that match would install a stranger's mod.
func TestDoesNotConfusePrivateModWithSameNamedProject(t *testing.T) {
	srv := newCollisionServer(t)
	r := New(Options{Modrinth: modrinth.New(modrinth.Options{BaseURL: srv.URL, HTTP: srv.Client()})})

	res := r.Resolve(context.Background(), Candidate{
		FileName: "moretools-1.0.0.jar",
		SHA1:     "deadbeef",
		Size:     2_708_089, // the private jar
		Meta: &modmeta.Meta{
			ModID:   "more-tools",
			Name:    "more-tools",
			Version: "1.0.0",
			Loader:  modmeta.LoaderFabric,
		},
	})

	if res.Resolution.Method != MethodUnmatched {
		t.Fatalf("expected the private mod to stay unmatched, got method=%q project=%q title=%q",
			res.Resolution.Method, res.Resolution.ProjectID, res.Resolution.Title)
	}
	if res.Err == nil {
		t.Error("expected an explanatory error for an unmatched mod")
	}
}

// A same-named project must still be accepted when the evidence agrees, so the
// corroboration checks do not reject genuine CurseForge-mirror jars.
func TestAcceptsSameNamedProjectWhenVersionAndSizeAgree(t *testing.T) {
	srv := newCollisionServer(t)
	r := New(Options{Modrinth: modrinth.New(modrinth.Options{BaseURL: srv.URL, HTTP: srv.Client()})})

	res := r.Resolve(context.Background(), Candidate{
		FileName: "common-networking-fabric-26.2-1.1.0.jar",
		SHA1:     "cafe1234",
		Size:     23_937,
		Meta: &modmeta.Meta{
			ModID:   "commonnetworking",
			Name:    "Common Network",
			Version: "26.2-1.1.0",
			Loader:  modmeta.LoaderFabric,
		},
	})

	if res.Resolution.Method == MethodUnmatched {
		t.Fatalf("expected a match, got unmatched (err %v)", res.Err)
	}
	// "cnet" is the fake project id; the slug is what we matched on.
	if res.Resolution.ProjectID != "cnet" {
		t.Errorf("ProjectID = %q, want cnet", res.Resolution.ProjectID)
	}
	if res.Resolution.Title != "Common Network" {
		t.Errorf("Title = %q, want Common Network", res.Resolution.Title)
	}
}

// A jar whose declared loader the project does not support is a hard veto,
// independent of how well the names line up.
func TestLoaderMismatchVetoesMatch(t *testing.T) {
	srv := newCollisionServer(t)
	r := New(Options{Modrinth: modrinth.New(modrinth.Options{BaseURL: srv.URL, HTTP: srv.Client()})})

	res := r.Resolve(context.Background(), Candidate{
		FileName: "forgemod-1.0.0.jar",
		SHA1:     "9999aaaa",
		Size:     200_000,
		Meta: &modmeta.Meta{
			ModID:   "forgemod",
			Name:    "ForgeMod",
			Version: "1.0.0",
			Loader:  modmeta.LoaderFabric, // the project is Forge-only
		},
	})

	if res.Resolution.Method != MethodUnmatched {
		t.Errorf("expected a Forge-only project to be vetoed for a Fabric jar, got %+v", res.Resolution)
	}
}

// An exact hash match is authoritative and must survive every heuristic.
func TestExactHashBypassesCorroboration(t *testing.T) {
	srv := newCollisionServer(t)
	r := New(Options{Modrinth: modrinth.New(modrinth.Options{BaseURL: srv.URL, HTTP: srv.Client()})})

	res := r.Resolve(context.Background(), Candidate{
		FileName: "sodium-fabric-0.9.1+mc26.2.jar",
		SHA1:     "sodiumhash",
		Size:     1_913_459,
		Meta: &modmeta.Meta{
			ModID:   "sodium",
			Name:    "Sodium",
			Version: "0.9.1",
			Loader:  modmeta.LoaderFabric,
		},
	})

	if res.Resolution.Method != MethodHash {
		t.Fatalf("expected an exact hash match, got %q", res.Resolution.Method)
	}
	if res.Resolution.Confidence < 1 {
		t.Errorf("hash matches must be fully confident, got %.2f", res.Resolution.Confidence)
	}
	if res.Resolution.Title != "Sodium" {
		t.Errorf("Title = %q, want Sodium", res.Resolution.Title)
	}
}

// newCollisionServer serves a fake Modrinth containing three projects chosen to
// collide with the test candidates:
//
//	more-tools      name collision, version 1.0.0 exists but 3.3x the size
//	common-network  legitimate match
//	forge-only      a project that does not support fabric
func newCollisionServer(t *testing.T) *httptest.Server {
	t.Helper()

	projects := map[string]modrinth.Project{
		"more-tools":     {ID: "polymer", Slug: "more-tools", Title: "More Tools (Polymer)", Loaders: []string{"fabric"}},
		"common-network": {ID: "cnet", Slug: "common-network", Title: "Common Network", Loaders: []string{"fabric"}},
		"forge-only":     {ID: "fonly", Slug: "forge-only", Title: "ForgeMod", Loaders: []string{"forge"}},
		"sodium":         {ID: "AANobbMI", Slug: "sodium", Title: "Sodium", Loaders: []string{"fabric"}},
	}
	versions := map[string][]modrinth.Version{
		"more-tools": {
			{ID: "poly-100", ProjectID: "polymer", VersionNumber: "1.0.0", VersionType: "release",
				Loaders: []string{"fabric"}, GameVersions: []string{"1.20.1"},
				Files: []modrinth.File{{Filename: "moretools-1.0.0.jar", Primary: true, Size: 8_876_666}}},
			{ID: "poly-latest", ProjectID: "polymer", VersionNumber: "1.10.1+26.2", VersionType: "release",
				Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
				Files: []modrinth.File{{Filename: "more-tools-1.10.1+26.2.jar", Primary: true, Size: 1_000_000}}},
		},
		"common-network": {
			{ID: "cn-1", ProjectID: "cnet", VersionNumber: "26.2-1.1.0", VersionType: "release",
				Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
				Files: []modrinth.File{{Filename: "common-networking-fabric-26.2-1.1.0.jar", Primary: true, Size: 23_937}}},
		},
		"forge-only": {
			{ID: "fo-1", ProjectID: "fonly", VersionNumber: "1.0.0", VersionType: "release",
				Loaders: []string{"forge"}, GameVersions: []string{"26.2"},
				Files: []modrinth.File{{Filename: "forgemod-1.0.0.jar", Primary: true, Size: 200_000}}},
		},
		"sodium": {
			{ID: "sod-1", ProjectID: "AANobbMI", VersionNumber: "mc26.2-0.9.1-fabric", VersionType: "release",
				Loaders: []string{"fabric"}, GameVersions: []string{"26.2"},
				Files: []modrinth.File{{Filename: "sodium-fabric-0.9.1+mc26.2.jar", Primary: true, Size: 1_913_459}}},
		},
	}

	// The real API accepts either a slug or a project id in the path. The
	// resolver always uses the id, so index versions by both.
	byID := map[string][]modrinth.Version{}
	for slug, vs := range versions {
		byID[slug] = vs
		byID[projects[slug].ID] = vs
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/version_file/", func(w http.ResponseWriter, r *http.Request) {
		sha := strings.TrimPrefix(r.URL.Path, "/version_file/")
		if sha != "sodiumhash" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, versions["sodium"][0])
	})
	mux.HandleFunc("/project/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/project/")
		if strings.HasSuffix(p, "/version") {
			writeJSON(w, byID[strings.TrimSuffix(p, "/version")])
			return
		}
		pr, ok := projects[p]
		if !ok {
			// Allow lookup by project id as well as slug.
			for _, v := range projects {
				if v.ID == p {
					writeJSON(w, v)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, pr)
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		var hits []modrinth.Hit
		for _, p := range projects {
			// Mirror the real API loosely: return everything as a candidate.
			hits = append(hits, modrinth.Hit{ProjectID: p.ID, Slug: p.Slug, Title: p.Title, Downloads: 1000})
		}
		_ = q
		writeJSON(w, modrinth.SearchResponse{Hits: hits, TotalHits: len(hits)})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
