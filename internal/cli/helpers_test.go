package cli

import (
	"strings"
	"testing"

	"github.com/MohammadMD1383/modharbor/internal/instance"
)

// The instance reference has one precedence order everywhere it is used:
// positional argument, then the global flag, then the config default. Any
// command that reorders these three silently operates on the wrong instance,
// and since the wrong instance is a real, valid directory the failure is
// invisible unless the command echoes it back.
func TestPickInstanceArgPrefersPositionalOverFlag(t *testing.T) {
	orig := flagInstance
	t.Cleanup(func() { flagInstance = orig })

	cases := []struct {
		name         string
		args         []string
		flagInstance string
		want         string
	}{
		{
			name:         "argument wins over the flag",
			args:         []string{"26.3-fabric-mod"},
			flagInstance: "26.2-fabric-mod",
			want:         "26.3-fabric-mod",
		},
		{
			// The flag is the only reference available here, so dropping it
			// would send the command at the config default instead.
			name:         "flag is used when no argument is given",
			args:         nil,
			flagInstance: "26.3-fabric-mod",
			want:         "26.3-fabric-mod",
		},
		{
			// A blank argument must not shadow the flag: an empty string is
			// not a reference, it is the absence of one.
			name:         "blank argument falls through to the flag",
			args:         []string{""},
			flagInstance: "26.3-fabric-mod",
			want:         "26.3-fabric-mod",
		},
		{
			name:         "both absent defers to the config default",
			args:         nil,
			flagInstance: "",
			want:         "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagInstance = tc.flagInstance
			if got := pickInstanceArg(tc.args); got != tc.want {
				t.Errorf("pickInstanceArg(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// The error the user sees when no instance could be determined at all is the
// only guidance that exists for a bare `modharbor list` on a fresh install, so
// it has to name both ways out.
//
// Note that no command currently calls requireInstanceArg — every one of them
// goes straight to ResolveInstance, whose own message is thinner and which
// carries no missingInstanceError, so Execute's "try: modharbor instances"
// hint is unreachable. See the report; this test pins the better message so
// wiring it up later cannot regress it.
func TestRequireInstanceArgExplainsBothWaysToNameAnInstance(t *testing.T) {
	orig := flagInstance
	t.Cleanup(func() { flagInstance = orig })

	flagInstance = ""
	_, err := requireInstanceArg(nil)
	if err == nil {
		t.Fatal("no error for a missing instance")
	}
	for _, want := range []string{"no instance specified", "argument", "config set defaultInstance"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not mention %q: %s", want, err)
		}
	}

	// The same call must stay silent-but-correct once a reference exists;
	// a spurious error here would abort commands that have everything they need.
	flagInstance = "26.3-fabric-mod"
	ref, err := requireInstanceArg(nil)
	if err != nil {
		t.Fatalf("unexpected error with the flag set: %v", err)
	}
	if ref != "26.3-fabric-mod" {
		t.Errorf("ref = %q, want the flag's value", ref)
	}
}

// loaderName feeds the Modrinth loader filter and loaderLabel feeds the output.
// They are separate functions because the display form has a fallback for a
// vanilla instance while the query form has to send nothing at all — sending
// "vanilla" upstream matches no build and silently hides every update.
func TestLoaderNamesCoverEveryInstanceType(t *testing.T) {
	cases := []struct {
		typ       instance.Type
		wantTag   string
		wantLabel string
	}{
		{instance.TypeVanilla, "", "vanilla"},
		{instance.TypeFabric, "fabric", "fabric"},
		{instance.TypeQuilt, "quilt", "quilt"},
		{instance.TypeForge, "forge", "forge"},
		{instance.TypeNeoForge, "neoforge", "neoforge"},
		// Unknown is rendered as vanilla because that is the actionable
		// reading: mods were found but no loader was detected.
		{instance.TypeUnknown, "", "vanilla"},
	}

	for _, tc := range cases {
		t.Run(string(tc.typ), func(t *testing.T) {
			if got := loaderName(tc.typ); got != tc.wantTag {
				t.Errorf("loaderName(%q) = %q, want %q", tc.typ, got, tc.wantTag)
			}
			if got := loaderLabel(tc.typ); got != tc.wantLabel {
				t.Errorf("loaderLabel(%q) = %q, want %q", tc.typ, got, tc.wantLabel)
			}
		})
	}
}

// A project reference reaches the CLI in every shape a user can paste. Missing
// a form means the Modrinth lookup 404s and the user is told the project does
// not exist, which is a false statement about a project that plainly does.
func TestProjectKeyAcceptsSlugsAndEveryModrinthURLForm(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare slug", "sodium", "sodium"},
		{"bare project id", "AANobbMI", "AANobbMI"},
		{"surrounding whitespace is ignored", "  sodium\t", "sodium"},
		{"https mod URL", "https://modrinth.com/mod/lithium", "lithium"},
		{"http mod URL", "http://modrinth.com/mod/lithium", "lithium"},
		{"project URL", "https://modrinth.com/project/lithium", "lithium"},
		{"trailing slash", "https://modrinth.com/mod/lithium/", "lithium"},
		{"query string on a bare slug", "lithium?tab=versions", "lithium"},
		{"query string on a URL", "https://modrinth.com/mod/lithium?tab=versions", "lithium"},
		{"fragment on a URL", "https://modrinth.com/mod/lithium#files", "lithium#files"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectKey(tc.in); got != tc.want {
				t.Errorf("projectKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// `remove` accepts whatever the user has in front of them: a display name, a
// mod id, a project id or a file name, in any case and with any separators.
// Every one of those spellings has to land on the same jar, because getting it
// wrong means deleting the wrong file.
func TestMatchModsResolvesEveryNamingStyle(t *testing.T) {
	rows := []ScannedMod{
		{
			FileName:  "sodium-0.10.0.jar",
			Title:     "Sodium",
			ModID:     "sodium",
			ProjectID: "AANobbMI",
		},
		{
			FileName:  "lithium-fabric-0.2.0.jar",
			Title:     "Lithium",
			ModID:     "lithium",
			ProjectID: "gvQqBUqZ",
		},
		{
			// Display names reach for spaces, hyphens and underscores
			// interchangeably, and the jar on disk carries none of them.
			FileName:  "inventoryprofilesnext-1.0.4.jar",
			Title:     "Inventory Profiles Next",
			ModID:     "inventoryprofilesnext",
			ProjectID: "qLQaVLjk",
		},
	}

	cases := []struct {
		name   string
		wanted []string
		want   []string // file names, in order
	}{
		{"display name", []string{"Sodium"}, []string{"sodium-0.10.0.jar"}},
		{"display name ignores case", []string{"lithium"}, []string{"lithium-fabric-0.2.0.jar"}},
		{"display name ignores separators", []string{"inventory-profiles_next"},
			[]string{"inventoryprofilesnext-1.0.4.jar"}},
		{"mod id", []string{"sodium"}, []string{"sodium-0.10.0.jar"}},
		{"project id", []string{"AANobbMI"}, []string{"sodium-0.10.0.jar"}},
		{"exact file name", []string{"sodium-0.10.0.jar"}, []string{"sodium-0.10.0.jar"}},
		{"file name prefix", []string{"sodium-0.10"}, []string{"sodium-0.10.0.jar"}},
		{"file name prefix ignores case", []string{"SODIUM-0.10"}, []string{"sodium-0.10.0.jar"}},
		{"several references resolve in order", []string{"lithium", "sodium"},
			[]string{"lithium-fabric-0.2.0.jar", "sodium-0.10.0.jar"}},
		{"blank references are dropped", []string{"", "   ", "sodium"}, []string{"sodium-0.10.0.jar"}},
		{"unknown reference matches nothing", []string{"nonexistent"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, m := range matchMods(rows, tc.wanted) {
				got = append(got, m.FileName)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("matchMods(%q) = %v, want %v", tc.wanted, got, tc.want)
			}
		})
	}
}

// Two jars presenting the same mod id are indistinguishable to matchMods, and
// the command deletes what it is handed. Taking the first row rather than
// erroring or returning both is a deliberate choice — deleting one of two
// copies is the right outcome — but it is only safe while the choice is
// deterministic, so it is pinned here.
func TestMatchModsResolvesAnAmbiguousReferenceToTheFirstRow(t *testing.T) {
	rows := []ScannedMod{
		{FileName: "b-sodium-1.0.jar", Title: "Sodium", ModID: "sodium"},
		{FileName: "a-sodium-2.0.jar", Title: "Sodium", ModID: "sodium"},
	}

	got := matchMods(rows, []string{"sodium"})
	if len(got) != 1 {
		t.Fatalf("matched %d jars, want exactly 1: %v", len(got), got)
	}
	if got[0].FileName != rows[0].FileName {
		t.Errorf("matched %q, want the first row %q", got[0].FileName, rows[0].FileName)
	}
}

// An empty display name has to render as something, because a blank cell in a
// table reads as a formatting bug rather than as missing data.
func TestTruncateNameSubstitutesADashAndKeepsShortNamesIntact(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"empty name becomes a dash", "", 30, "—"},
		{"short name is untouched", "Sodium", 30, "Sodium"},
		// Exactly at the limit is not truncated: an ellipsis here would imply
		// there was more text hidden.
		{"name exactly at the limit", "0123456789", 10, "0123456789"},
		{"long name gains an ellipsis", strings.Repeat("x", 40), 30,
			strings.Repeat("x", 29) + "…"},
		// A zero width would otherwise let ui.Truncate swallow the name
		// entirely, taking the only label the row had.
		{"zero width yields nothing", "Sodium", 0, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateName(tc.in, tc.width); got != tc.want {
				t.Errorf("truncateName(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
		})
	}
}

// orDash exists so "no value" and "a value of spaces" never render the same.
// A whitespace value is data; only the empty string is absence.
func TestOrDashReplacesOnlyTheEmptyString(t *testing.T) {
	cases := map[string]string{
		"":       "—",
		"sodium": "sodium",
		" ":      " ",
	}
	for in, want := range cases {
		if got := orDash(in); got != want {
			t.Errorf("orDash(%q) = %q, want %q", in, got, want)
		}
	}
}

// The total is the sum over every row including the unidentified ones. A jar
// modharbor could not identify still occupies disk, and a total that quietly
// excluded it would understate the space `update` cannot reclaim.
func TestTotalSizeSumsEveryRowIncludingUnknownMods(t *testing.T) {
	cases := []struct {
		name string
		rows []ScannedMod
		want int64
	}{
		{"no rows", nil, 0},
		{"one row", []ScannedMod{{Size: 1500}}, 1500},
		{"several rows", []ScannedMod{{Size: 10}, {Size: 20}, {Size: 12}}, 42},
		{"unknown mods still count", []ScannedMod{{Size: 7, Method: "unmatched"}, {Size: 3}}, 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := totalSize(tc.rows); got != tc.want {
				t.Errorf("totalSize() = %d, want %d", got, tc.want)
			}
		})
	}
}
