package modrinth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, HTTP: srv.Client()}), srv
}

func TestVersionByHash(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version_file/abc123" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, Version{
			ID:            "v1",
			ProjectID:     "sodium",
			VersionNumber: "0.9.1",
			VersionType:   "release",
			Loaders:       []string{"fabric"},
			GameVersions:  []string{"26.2"},
			Files: []File{{
				Filename: "sodium-fabric-0.9.1.jar",
				Primary:  true,
				Size:     1234,
				Hashes:   map[string]string{"sha1": "abc123"},
			}},
		})
	})
	v, err := c.VersionByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if v.ProjectID != "sodium" {
		t.Errorf("ProjectID = %q", v.ProjectID)
	}
	f, ok := v.PrimaryFile()
	if !ok || f.Filename != "sodium-fabric-0.9.1.jar" {
		t.Errorf("PrimaryFile = %+v %v", f, ok)
	}
	if !v.Supports("26.2", "fabric") {
		t.Error("expected Supports(26.2, fabric) = true")
	}
	if v.Supports("1.20.1", "fabric") {
		t.Error("expected Supports(1.20.1, fabric) = false")
	}
	if v.Supports("26.2", "forge") {
		t.Error("expected Supports(26.2, forge) = false")
	}
}

func TestVersionByHashNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := c.VersionByHash(context.Background(), "deadbeef"); err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

func TestVersionsFiltersByGameVersionAndLoader(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		writeJSON(t, w, []Version{{ID: "v2", VersionNumber: "0.9.3"}})
	})
	_, err := c.Versions(context.Background(), "sodium", VersionListOptions{
		GameVersions: []string{"26.3"},
		Loaders:      []string{"fabric"},
		Channel:      ChannelRelease,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"game_versions", "loaders"} {
		if !contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	// version_type must NOT be sent: Modrinth accepts only one value there,
	// and filtering server-side would hide pre-release builds that do
	// support the target game version.
	if contains(gotQuery, "version_type") {
		t.Errorf("query %q must not contain version_type; filtering is client-side", gotQuery)
	}
}

func TestFilterByChannelKeepsOnlyAllowedTypes(t *testing.T) {
	vers := []Version{
		{ID: "a", VersionType: "alpha", DatePublished: time.Now().Add(3 * time.Hour)},
		{ID: "b", VersionType: "beta", DatePublished: time.Now().Add(2 * time.Hour)},
		{ID: "c", VersionType: "release", DatePublished: time.Now()},
	}

	got := FilterByChannel(vers, ChannelRelease)
	if len(got) != 1 || got[0].ID != "c" {
		t.Errorf("release channel = %+v, want only c", ids(got))
	}

	got = FilterByChannel(vers, ChannelBeta)
	if len(got) != 2 || got[0].ID != "b" {
		t.Errorf("beta channel = %+v, want b then c", ids(got))
	}

	got = FilterByChannel(vers, ChannelAlpha)
	if len(got) != 3 {
		t.Errorf("alpha channel = %+v, want all three", ids(got))
	}
	// Newest first, regardless of type.
	if got[0].ID != "a" {
		t.Errorf("alpha channel order = %+v, want newest first", ids(got))
	}
}

func TestTypeRankPrefersReleases(t *testing.T) {
	if TypeRank("release", ChannelAlpha) != 0 {
		t.Error("release should rank first even on the alpha channel")
	}
	if TypeRank("alpha", ChannelAlpha) != 2 {
		t.Error("alpha should rank last on the alpha channel")
	}
	if !AllowsChannel("beta", ChannelBeta) {
		t.Error("beta must be allowed on the beta channel")
	}
	if AllowsChannel("alpha", ChannelBeta) {
		t.Error("alpha must not be allowed on the beta channel")
	}
}

func ids(v []Version) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = x.ID
	}
	return out
}

func TestVersionsBatching(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/versions" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([]Version, 0, len(body.IDs))
		for _, id := range body.IDs {
			out = append(out, Version{ID: id})
		}
		writeJSON(t, w, out)
	})
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = "id" + string(rune('a'+i%26))
	}
	got, err := c.VersionsBatch(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 {
		t.Errorf("got %d versions, want 250", len(got))
	}
}

func TestSearchBuildsFacets(t *testing.T) {
	var facets []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.Unmarshal([]byte(r.URL.Query().Get("facets")), &facets)
		writeJSON(t, w, SearchResponse{Hits: []Hit{{ProjectID: "sodium", Slug: "sodium"}}, TotalHits: 1})
	})
	res, err := c.Search(context.Background(), "sodium", SearchOptions{
		Limit: 10, GameVersion: "26.3", Loader: "fabric",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Slug != "sodium" {
		t.Errorf("unexpected hits %+v", res.Hits)
	}
	if len(facets) < 2 {
		t.Errorf("expected version and loader facets, got %v", facets)
	}
}

func TestCDNURLResolution(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data/abc" {
			writeJSON(t, w, map[string]string{"url": "https://cdn.modrinth.com/data/real"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	got, err := c.CDNURL(context.Background(), File{URL: "https://api.modrinth.com/v2/data/abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cdn.modrinth.com/data/real" {
		t.Errorf("CDNURL = %q", got)
	}
}

func TestCDNURLFallsBackToMirror(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mirror := "https://api.modrinth.com/v2/data/abc"
	got, err := c.CDNURL(context.Background(), File{URL: mirror})
	if err != nil {
		t.Fatal(err)
	}
	if got != mirror {
		t.Errorf("expected fallback to %q, got %q", mirror, got)
	}
}

func TestUserAgentIsSent(t *testing.T) {
	var ua string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		writeJSON(t, w, Project{ID: "sodium", Title: "Sodium"})
	})
	if _, err := c.Project(context.Background(), "sodium"); err != nil {
		t.Fatal(err)
	}
	if ua == "" || ua == "Go-http-client/1.1" {
		t.Errorf("User-Agent = %q, want a descriptive value", ua)
	}
}

func TestInMemoryCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(t, w, Project{ID: "sodium", Title: "Sodium"})
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, HTTP: srv.Client(), CacheTTL: time.Minute})

	for i := 0; i < 3; i++ {
		if _, err := c.Project(context.Background(), "sodium"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call with caching, got %d", calls)
	}
}

func TestRetryOnServerError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(t, w, Project{ID: "sodium"})
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, HTTP: srv.Client()})

	if _, err := c.Project(context.Background(), "sodium"); err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected retries, got %d calls", calls)
	}
}

func TestAllowedChannels(t *testing.T) {
	cases := map[Channel]int{
		ChannelRelease: 1,
		ChannelBeta:    2,
		ChannelAlpha:   3,
	}
	for ch, want := range cases {
		if got := len(allowedChannels(ch)); got != want {
			t.Errorf("allowedChannels(%s) = %d, want %d", ch, got, want)
		}
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
