package version

import (
	"strings"
	"testing"
)

// The version banner is the first thing `modharbor version` prints, so the
// formatting rules are pinned here: an unknown commit is omitted rather than
// printed literally, a full hash is shortened, and the build date line only
// appears when the linker actually set it.
func TestStringOmitsUnknownCommit(t *testing.T) {
	origVersion, origCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = origVersion, origCommit })

	Version, Commit = "0.1.0", "unknown"
	if got := String(); got != "0.1.0" {
		t.Errorf("String() = %q, want %q", got, "0.1.0")
	}
}

func TestStringShortensFullCommit(t *testing.T) {
	origVersion, origCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = origVersion, origCommit })

	Version, Commit = "0.1.0", "abcdef1234567890"
	if got, want := String(), "0.1.0 (abcdef1)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringKeepsShortCommitWhole(t *testing.T) {
	origVersion, origCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = origVersion, origCommit })

	Version, Commit = "0.1.0", "abc"
	if got, want := String(), "0.1.0 (abc)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestBannerOmitsBuiltLineWithoutDate(t *testing.T) {
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origVersion, origCommit, origDate })

	Version, Commit, Date = "0.1.0", "unknown", "unknown"
	got := Banner()
	if !strings.HasPrefix(got, "modharbor 0.1.0\n") {
		t.Errorf("Banner() = %q, want prefix %q", got, "modharbor 0.1.0\n")
	}
	if strings.Contains(got, "built:") {
		t.Errorf("Banner() = %q, want no built line when Date is unknown", got)
	}
	if !strings.Contains(got, "runtime:") {
		t.Errorf("Banner() = %q, want a runtime line always", got)
	}
}

func TestBannerIncludesDateWhenSet(t *testing.T) {
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origVersion, origCommit, origDate })

	Version, Commit, Date = "1.2.3", "abcdef1234567890", "2026-10-03T00:00:00Z"
	got := Banner()
	if !strings.Contains(got, "modharbor 1.2.3 (abcdef1)") {
		t.Errorf("Banner() = %q, want version with short commit", got)
	}
	if !strings.Contains(got, "built:    2026-10-03T00:00:00Z") {
		t.Errorf("Banner() = %q, want the build date line", got)
	}
}

func TestGoVersionLooksLikeAToolchain(t *testing.T) {
	if got := GoVersion(); !strings.HasPrefix(got, "go") {
		t.Errorf("GoVersion() = %q, want a string starting with %q", got, "go")
	}
}
