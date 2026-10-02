package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMissingFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.AllResolutions()) != 0 {
		t.Error("a fresh store must have no resolutions")
	}
	// Saving an untouched store must not create anything on disk.
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an untouched store should not create a state file")
	}

	// Once there is something to record, the directory is created.
	s.Put(Resolution{SHA1: "abc", ProjectID: "p"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected the state file to be created: %v", err)
	}
}

func TestResolutionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Put(Resolution{SHA1: "ABCdef0123", ProjectID: "AANobbMI", Title: "Sodium", Method: "hash"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Lookup("abcdef0123")
	if !ok {
		t.Fatal("expected the resolution to survive a round trip")
	}
	// Lookup must be case-insensitive because digests come from many sources.
	if got.ProjectID != "AANobbMI" || got.Title != "Sodium" {
		t.Errorf("unexpected resolution %+v", got)
	}
}

func TestSaveIsAtomicAndSkippedWhenClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, _ := Open(path)
	// No changes yet: Save must not write anything.
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("Save should be a no-op when nothing changed")
	}

	s.Put(Resolution{SHA1: "aa", ProjectID: "p"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// A second Save with no further changes must not rewrite the file.
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("an unchanged store should not be rewritten")
	}

	// No temp files may be left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("unexpected leftover file %q", e.Name())
		}
	}
}

func TestForgetRemovesEntry(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	s.Put(Resolution{SHA1: "deadbeef", ProjectID: "p"})
	if _, ok := s.Lookup("deadbeef"); !ok {
		t.Fatal("setup failed")
	}
	s.Forget("DEADBEEF")
	if _, ok := s.Lookup("deadbeef"); ok {
		t.Error("Forget should be case-insensitive")
	}
}

func TestCorruptStateFileIsQuarantinedNotFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("a corrupt cache must never block the tool: %v", err)
	}
	if len(s.AllResolutions()) != 0 {
		t.Error("expected an empty store after recovering from corruption")
	}
	// The bad file must be kept for debugging.
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("expected the corrupt file to be preserved: %v", err)
	}
}

func TestJSONCacheRespectsTTL(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))

	var out struct{ N int }
	if s.GetJSON("k", &out) {
		t.Error("expected a miss on an empty cache")
	}
	s.PutJSON("k", map[string]int{"n": 7}, time.Minute)
	if !s.GetJSON("k", &out) || out.N != 7 {
		t.Errorf("expected a hit, got %+v", out)
	}

	s.PutJSON("expired", map[string]int{"n": 1}, -time.Second)
	if s.GetJSON("expired", &out) {
		t.Error("expected a non-positive TTL to be ignored rather than cached")
	}
}

func TestPruneExpiredDropsStaleEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write a document containing one already-expired entry by hand, since
	// PutJSON refuses to store one.
	doc := Document{
		SchemaVersion: SchemaVersion,
		Resolutions:   map[string]Resolution{},
		Cache: map[string]CacheEntry{
			"stale": {Key: "stale", ExpiresAt: time.Now().Add(-time.Hour), Value: []byte(`{"n":1}`)},
			"fresh": {Key: "fresh", ExpiresAt: time.Now().Add(time.Hour), Value: []byte(`{"n":2}`)},
		},
		Instances: map[string]InstanceRecord{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := s.PruneExpired(); n != 1 {
		t.Errorf("PruneExpired = %d, want 1", n)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, _ := Open(path)
	if _, ok := reopened.doc.Cache["stale"]; ok {
		t.Error("the stale entry should not have been persisted")
	}
	var out struct{ N int }
	if !reopened.GetJSON("fresh", &out) || out.N != 2 {
		t.Error("the fresh entry must be preserved")
	}
}

func TestPutJSONIgnoresNonPositiveTTL(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	s.PutJSON("k", 1, 0)
	var v int
	if s.GetJSON("k", &v) {
		t.Error("a zero TTL must not cache")
	}
}

func TestInstanceRecordsMergeInsteadOfClobber(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	path := "/minecraft/versions/x"

	s.PutInstance(InstanceRecord{Path: path, InstanceID: "x", MCVersion: "26.2", ModCount: 30, LastScan: time.Now()})
	// A later call that only sets ModCount must not erase the rest.
	s.PutInstance(InstanceRecord{Path: path, ModCount: 31})

	got, ok := s.Instance(path)
	if !ok {
		t.Fatal("expected the record to exist")
	}
	if got.MCVersion != "26.2" || got.InstanceID != "x" {
		t.Errorf("existing fields were lost: %+v", got)
	}
	if got.ModCount != 31 {
		t.Errorf("ModCount = %d, want the updated 31", got.ModCount)
	}
	if got.LastScan.IsZero() {
		t.Error("LastScan should have been preserved")
	}
}

func TestStatsReflectContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	s.Put(Resolution{SHA1: "a", ProjectID: "p"})
	s.Put(Resolution{SHA1: "b", ProjectID: "q"})
	s.PutInstance(InstanceRecord{Path: "/a"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	st := s.Stats()
	if st.Resolutions != 2 || st.Instances != 1 {
		t.Errorf("unexpected stats %+v", st)
	}
	if st.SizeBytes <= 0 {
		t.Error("SizeBytes should be measured from disk")
	}
}

func TestAllResolutionsIsDeterministicallyOrdered(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	for _, sha := range []string{"ccc", "aaa", "bbb"} {
		s.Put(Resolution{SHA1: sha, ProjectID: "p"})
	}
	got := s.AllResolutions()
	for i := 1; i < len(got); i++ {
		if got[i-1].SHA1 > got[i].SHA1 {
			t.Fatalf("resolutions are not sorted: %v", got)
		}
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				s.Put(Resolution{SHA1: string(rune('a'+i)) + string(rune('0'+j%10)), ProjectID: "p"})
				s.Lookup("a0")
				s.PutJSON("k", j, time.Minute)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// The saved document must still be valid JSON.
	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("concurrent writes produced invalid JSON: %v", err)
	}
}
