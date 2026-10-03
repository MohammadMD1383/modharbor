package migrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// ─── parseNumericVersion ─────────────────────────────────────────────────────

// TestParseNumericVersion pins the leading dotted-numeric extraction. The
// bare-number and non-numeric rejections matter: a Minecraft version like
// "26.2" must compare as a version, while a build date or a channel name must
// not be mistaken for one.
func TestParseNumericVersion(t *testing.T) {
	cases := []struct {
		in     string
		want   []int
		wantOK bool
	}{
		{"0.154.2+26.2", []int{0, 154, 2}, true},
		{"1.20.1", []int{1, 20, 1}, true},
		{"0.9.3-alpha.1", []int{0, 9, 3}, true},
		{"  2.16.1  ", []int{2, 16, 1}, true},
		{"v3.3.0", []int{3, 3, 0}, true},
		{"26.2", []int{26, 2}, true},
		{"1.2.3-beta", []int{1, 2, 3}, true},
		{"1.2.", []int{1, 2}, true}, // trailing dot is not a component
		{"", nil, false},            //
		{"   ", nil, false},         // whitespace only
		{"2026", nil, false},        // a bare number is not a version
		{"1", nil, false},           //
		{"v", nil, false},           // the "v" prefix alone leaves nothing
		{"alpha", nil, false},       //
		{".1.2", nil, false},        // no leading component
		{"1..2", nil, false},        // an empty component aborts the run
		{"1.2.3.4.5", []int{1, 2, 3, 4, 5}, true},
	}
	for _, c := range cases {
		got, ok := parseNumericVersion(c.in)
		if ok != c.wantOK {
			t.Errorf("parseNumericVersion(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parseNumericVersion(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseNumericVersion(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// TestAtoiOrZeroStopsAtTheFirstNonDigit pins the accumulator, including the
// overflow bail-out that keeps a pathologically long component from wrapping
// into a small (and therefore "older") number.
func TestAtoiOrZeroStopsAtTheFirstNonDigit(t *testing.T) {
	cases := map[string]int{
		"":    0,
		"0":   0,
		"154": 154,
		"007": 7,
		"12a": 12, // stops at the 'a' and keeps what it had
		"9x9": 9,  //
		"1e3": 1,  // not exponent notation
	}
	for in, want := range cases {
		if got := atoiOrZero(in); got != want {
			t.Errorf("atoiOrZero(%q) = %d, want %d", in, got, want)
		}
	}
	if got := atoiOrZero(strings.Repeat("9", 20)); got <= 1<<30 {
		t.Errorf("atoiOrZero(20 nines) = %d, want it to bail out above 1<<30", got)
	}
}

// ─── version ordering ────────────────────────────────────────────────────────

// TestCompareVersionStringsIsNumericNotLexical is the reason this function
// exists: "0.9.3" is older than "0.10.0" even though it sorts later as text.
// Getting it backwards silently demotes every 0.10+ release to a duplicate.
func TestCompareVersionStringsIsNumericNotLexical(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.9.3", "0.10.0", -1},
		{"0.10.0", "0.9.3", 1},
		{"0.154.2+26.2", "0.154.10+26.2", -1},
		{"1.2.3", "1.2.3", 0},
		{"1.0.0", "1.0", 1}, // equal numeric core, then string order
		{"26.2", "26.10", -1},
		// A numeric version outranks an opaque one, whichever way round.
		{"1.2.3", "nightly", 1},
		{"nightly", "1.2.3", -1},
		{"", "1.2.3", -1},
		{"", "", 0},
		{"alpha", "beta", -1},
	}
	for _, c := range cases {
		if got := compareVersionStrings(c.a, c.b); got != c.want {
			t.Errorf("compareVersionStrings(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareFreshnessTieBreaks(t *testing.T) {
	// Version wins outright.
	if got := compareFreshness(
		Result{SourceVersion: "1.2.0", TargetFile: "short.jar"},
		Result{SourceVersion: "1.1.0", TargetFile: "a-much-longer-name.jar"},
	); got != 1 {
		t.Errorf("compareFreshness(newer version) = %d, want 1", got)
	}

	// Equal versions: the longer destination name wins, on the theory that it
	// is the more complete build.
	if got := compareFreshness(
		Result{SourceVersion: "1.0.0", TargetFile: "sodium-fabric-0.9.3.jar"},
		Result{SourceVersion: "1.0.0", TargetFile: "sodium.jar"},
	); got != 1 {
		t.Errorf("compareFreshness(longer target) = %d, want 1", got)
	}
	if got := compareFreshness(
		Result{SourceVersion: "1.0.0", TargetFile: "sodium.jar"},
		Result{SourceVersion: "1.0.0", TargetFile: "sodium-fabric-0.9.3.jar"},
	); got != -1 {
		t.Errorf("compareFreshness(shorter target) = %d, want -1", got)
	}

	// Fully tied apart from the source name, which makes the choice stable
	// rather than dependent on map iteration order.
	if got := compareFreshness(
		Result{SourceFile: "b.jar"},
		Result{SourceFile: "a.jar"},
	); got != 1 {
		t.Errorf("compareFreshness(source name) = %d, want 1", got)
	}
	if got := compareFreshness(Result{SourceFile: "a.jar"}, Result{SourceFile: "a.jar"}); got != 0 {
		t.Errorf("compareFreshness(identical) = %d, want 0", got)
	}
}

// TestPickWinnerKeepsNewestWhateverTheOrder guards the fold in pickWinner: it
// must not simply keep the first or the last entry it sees.
func TestPickWinnerKeepsNewestWhateverTheOrder(t *testing.T) {
	results := []Result{
		{SourceFile: "oldest.jar", SourceVersion: "0.9.1"},
		{SourceFile: "newest.jar", SourceVersion: "0.10.0"},
		{SourceFile: "middle.jar", SourceVersion: "0.9.5"},
	}
	for _, perm := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 0, 2}, {0, 2, 1}} {
		idx := perm
		if got := results[pickWinner(results, idx)].SourceVersion; got != "0.10.0" {
			t.Errorf("pickWinner(%v) kept %q, want 0.10.0", idx, got)
		}
	}
}

// ─── dedupeByProject ─────────────────────────────────────────────────────────

func installResult(project, title, file, target, ver string) Result {
	return Result{
		Action: ActionInstall, Title: title, ProjectID: project,
		SourceFile: file, TargetFile: target, SourceVersion: ver, TargetVersion: ver,
	}
}

// TestDedupeByProjectKeepsTheNewestAndNeverDropsSilently covers the canonical
// case: two copies of one mod in the source folder. The newer survives, the
// older is reported as a duplicate, and both stay in the report.
func TestDedupeByProjectKeepsTheNewestAndNeverDropsSilently(t *testing.T) {
	in := []Result{
		installResult("P7dR8mSH", "Fabric API", "fabric-api-0.154.2.jar", "fabric-api-0.161.0.jar", "0.154.2"),
		installResult("P7dR8mSH", "Fabric API", "fabric-api-0.161.0.jar", "fabric-api-0.161.0.jar", "0.161.0"),
		installResult("AANobbMI", "Sodium", "sodium-fabric-0.9.3.jar", "sodium-fabric-0.9.3.jar", "0.9.3"),
	}
	out := dedupeByProject(in)
	if len(out) != 3 {
		t.Fatalf("got %d results, want 3 — duplicates must stay visible: %+v", len(out), out)
	}

	byFile := map[string]Result{}
	for _, r := range out {
		byFile[r.SourceFile] = r
	}
	// Input order is preserved.
	if out[0].SourceFile != "fabric-api-0.154.2.jar" || out[1].SourceFile != "fabric-api-0.161.0.jar" {
		t.Errorf("result order changed: %q, %q", out[0].SourceFile, out[1].SourceFile)
	}

	old := byFile["fabric-api-0.154.2.jar"]
	if old.Action != ActionDuplicate {
		t.Errorf("older copy action = %q, want duplicate", old.Action)
	}
	if want := "same mod as Fabric API; would overwrite fabric-api-0.161.0.jar"; old.Reason != want {
		t.Errorf("older copy reason = %q, want %q", old.Reason, want)
	}
	if byFile["fabric-api-0.161.0.jar"].Action != ActionInstall {
		t.Errorf("newest copy action = %q, want install", byFile["fabric-api-0.161.0.jar"].Action)
	}
	// An unrelated project is untouched.
	if byFile["sodium-fabric-0.9.3.jar"].Action != ActionInstall {
		t.Errorf("sodium action = %q, want install", byFile["sodium-fabric-0.9.3.jar"].Action)
	}
}

// TestDedupeByProjectMarksEveryExtraCopy checks a folder holding three copies
// of one mod: exactly one winner, and both losers blame that same winner.
func TestDedupeByProjectMarksEveryExtraCopy(t *testing.T) {
	in := []Result{
		installResult("P7dR8mSH", "Fabric API", "a.jar", "api.jar", "0.154.2"),
		installResult("P7dR8mSH", "Fabric API", "b.jar", "api.jar", "0.161.0"),
		installResult("P7dR8mSH", "Fabric API", "c.jar", "api.jar", "0.163.0"),
	}
	out := dedupeByProject(in)
	if len(out) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(out), out)
	}
	winners, dupes := 0, 0
	for _, r := range out {
		switch r.Action {
		case ActionDuplicate:
			dupes++
			if !strings.Contains(r.Reason, "would overwrite api.jar") {
				t.Errorf("%s reason = %q, want it to name the overwritten file", r.SourceFile, r.Reason)
			}
		default:
			winners++
		}
	}
	if winners != 1 || dupes != 2 {
		t.Errorf("winners = %d, duplicates = %d, want 1 and 2", winners, dupes)
	}
	if out[2].SourceFile != "c.jar" || out[2].Action == ActionDuplicate {
		t.Errorf("expected c.jar (0.163.0) to survive, got %+v", out[2])
	}
}

// TestDedupeByProjectNamesTheVersionWhenNoFileIsInPlay covers the duplicate
// branch that names the surviving version, used when the copies have no
// destination file yet — a skip, or an unmatched mod carried over.
func TestDedupeByProjectNamesTheVersionWhenNoFileIsInPlay(t *testing.T) {
	skipped := func(file, ver string) Result {
		return Result{
			Action: ActionSkip, Title: "Old Mod", ProjectID: "oldmod",
			SourceFile: file, SourceVersion: ver, Reason: "no 26.3 build",
		}
	}
	out := dedupeByProject([]Result{
		skipped("old-1.0.0.jar", "1.0.0"),
		skipped("old-1.1.0.jar", "1.1.0"),
		// No version anywhere: the reason has to admit it.
		{Action: ActionSkip, Title: "No Version", ProjectID: "nover", SourceFile: "nv.jar"},
		{Action: ActionSkip, Title: "No Version", ProjectID: "nover", SourceFile: "nv2.jar"},
	})
	if len(out) != 4 {
		t.Fatalf("got %d results, want 4: %+v", len(out), out)
	}
	if got, want := out[0].Reason, "duplicate of Old Mod (1.1.0)"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	// With no version anywhere to quote, the reason has to admit it.
	for _, r := range out[2:] {
		if r.Action == ActionDuplicate {
			if got, want := r.Reason, "duplicate of No Version (unknown version)"; got != want {
				t.Errorf("%s reason = %q, want %q", r.SourceFile, got, want)
			}
			return
		}
	}
	t.Errorf("expected one of the version-less pair to be marked duplicate: %+v", out[2:])
}

// TestDedupeByProjectLeavesDistinctResultsAlone checks the no-op path returns
// the input untouched rather than rebuilding it.
func TestDedupeByProjectLeavesDistinctResultsAlone(t *testing.T) {
	in := []Result{
		installResult("AANobbMI", "Sodium", "sodium.jar", "sodium.jar", "0.9.3"),
		installResult("gvQqRqAc", "Lithium", "lithium.jar", "lithium.jar", "0.6.9"),
		// Same project, but nothing to collapse: only one entry.
		{Action: ActionSkip, Title: "Old Mod", ProjectID: "oldmod", SourceFile: "old.jar"},
		// Copies group by source file name, so two unknown jars never merge.
		{Action: ActionCopy, SourceFile: "mine-a.jar", TargetFile: "mine-a.jar"},
		{Action: ActionCopy, SourceFile: "mine-b.jar", TargetFile: "mine-b.jar"},
	}
	out := dedupeByProject(in)
	if len(out) != len(in) {
		t.Fatalf("got %d results, want %d: %+v", len(out), len(in), out)
	}
	for i := range in {
		if out[i].Action != in[i].Action || out[i].SourceFile != in[i].SourceFile {
			t.Errorf("result %d changed: %+v, want %+v", i, out[i], in[i])
		}
	}
	if dedupeByProject(nil) != nil {
		t.Error("dedupeByProject(nil) should stay nil")
	}
}

// TestDedupeKey pins the grouping unit: an identified mod groups by upstream
// project, everything else by source file name.
func TestDedupeKey(t *testing.T) {
	if got, want := dedupeKey(Result{ProjectID: "AANobbMI"}), "project:AANobbMI"; got != want {
		t.Errorf("dedupeKey(identified) = %q, want %q", got, want)
	}
	// A copy is grouped by file name exactly like an unresolved mod; two
	// different private jars must never be treated as one.
	copyKey := dedupeKey(Result{Action: ActionCopy, SourceFile: "mine.jar"})
	otherKey := dedupeKey(Result{Action: ActionSkip, SourceFile: "mine.jar"})
	if copyKey != "file:mine.jar" || otherKey != "file:mine.jar" {
		t.Errorf("dedupeKey(copy) = %q and dedupeKey(skip) = %q, want both file:mine.jar", copyKey, otherKey)
	}
	if dedupeKey(Result{SourceFile: "a.jar"}) == dedupeKey(Result{SourceFile: "b.jar"}) {
		t.Error("different jars must not share a dedupe key")
	}
}

func TestWinnerVersionAndOrUnknown(t *testing.T) {
	if got := winnerVersion(Result{SourceVersion: "1.0.0", TargetVersion: "2.0.0"}); got != "1.0.0" {
		t.Errorf("winnerVersion() = %q, want the source version", got)
	}
	if got := winnerVersion(Result{TargetVersion: "2.0.0"}); got != "2.0.0" {
		t.Errorf("winnerVersion() = %q, want the target version when the source has none", got)
	}
	if got := orUnknown(""); got != "unknown version" {
		t.Errorf("orUnknown(\"\") = %q, want %q", got, "unknown version")
	}
	if got := orUnknown("1.2.3"); got != "1.2.3" {
		t.Errorf("orUnknown(\"1.2.3\") = %q, want it unchanged", got)
	}
}

// ─── end to end ──────────────────────────────────────────────────────────────

// TestRunCollapsesTwoCopiesOfOneMod drives the whole path: two jars of the
// same project in the source folder both resolve to one destination file, and
// only the newest copy is carried over.
//
// The newer jar is deliberately named so that it sorts *before* the older one.
// If version comparison were skipped in favour of the name tie-break, the
// older jar would win whichever order the two arrive in, so this test fails.
func TestRunCollapsesTwoCopiesOfOneMod(t *testing.T) {
	f := newFakeMR(t)
	file := f.addMod("sodium", "sodium", "Sodium", "26.3", "0.9.3", "fabric")

	root := t.TempDir()
	src := makeInstance(t, root, "26.2-fabric-mod", "26.2", instance.TypeFabric)
	dst := makeInstance(t, root, "26.3-fabric-mod", "26.3", instance.TypeFabric)

	writeJar(t, src.ModsDirOrDefault(), "aaa-sodium-0.9.3.jar", "sodium", "0.9.3")
	writeJar(t, src.ModsDirOrDefault(), "zzz-sodium-0.9.1.jar", "sodium", "0.9.1")

	// Identify both jars up front so each carries its own version; without
	// this both would resolve to the same upstream build and the tie-break
	// would fall through to file names.
	eng, st := newEngineWithStore(t, f)
	for name, ver := range map[string]string{
		"aaa-sodium-0.9.3.jar": "0.9.3",
		"zzz-sodium-0.9.1.jar": "0.9.1",
	} {
		b, err := os.ReadFile(filepath.Join(src.ModsDirOrDefault(), name))
		if err != nil {
			t.Fatal(err)
		}
		st.Put(store.Resolution{
			SHA1: hashutil.SHA1Bytes(b), Provider: "modrinth", ProjectID: "sodium",
			VersionNumber: ver, Method: "hash", Confidence: 1.0,
		})
	}

	rep, err := eng.Run(context.Background(), src, dst, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 1 || rep.Duplicates != 1 {
		t.Fatalf("Installed = %d, Duplicates = %d, want 1 and 1 (%+v)", rep.Installed, rep.Duplicates, rep.Results)
	}
	byFile := map[string]Result{}
	for _, r := range rep.Results {
		byFile[r.SourceFile] = r
	}
	if got, want := byFile["aaa-sodium-0.9.3.jar"].Action, ActionInstall; got != want {
		t.Errorf("newer jar action = %q, want %q", got, want)
	}
	old := byFile["zzz-sodium-0.9.1.jar"]
	if old.Action != ActionDuplicate {
		t.Errorf("older jar action = %q, want duplicate", old.Action)
	}
	if !strings.Contains(old.Reason, file.Filename) {
		t.Errorf("reason = %q, want it to name %q", old.Reason, file.Filename)
	}
	// A duplicate is never planned work.
	for _, r := range rep.Planned() {
		if r.SourceFile == "zzz-sodium-0.9.1.jar" {
			t.Error("the duplicate was included in Planned()")
		}
	}
}
