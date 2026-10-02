package cli

import "testing"

// The dependency range parser decides whether doctor reports a mod as
// targeting the wrong Minecraft version. Being wrong in either direction is
// costly: a false "incompatible" makes users delete working mods.
func TestMCVersionFromRange(t *testing.T) {
	cases := map[string]string{
		// Bounded ranges name a target.
		"~26.2":      "26.2",
		"26.2":       "26.2",
		"~1.20.1":    "1.20.1",
		"=26.3":      "26.3",
		"26.1.2":     "26.1.2",
		"~26.2-":     "26.2",
		"~26.2+26.3": "26.2", // build metadata is not the target

		// Minimums are not targets. These appear constantly on mods that
		// support "this version or newer", including ones installed for a
		// newer release than the minimum.
		">=1.19.4": "",
		">=26.1-":  "",
		">26.2":    "",
		">1.20":    "",

		// Intervals have no single target.
		"[1.20.1,1.21)": "",
		"[26.2,26.3)":   "",
		"(,1.21]":       "",

		// Nothing usable.
		"*":        "",
		"":         "",
		"any":      "",
		"26.3-rc1": "", // a suffix is not a release line we can compare
	}
	for in, want := range cases {
		if got := mcVersionFromRange(in); got != want {
			t.Errorf("mcVersionFromRange(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameMCRelease(t *testing.T) {
	if !sameMCRelease("26.3", "26.3") {
		t.Error("identical releases must compare equal")
	}
	// Patch components are ignored: 26.3.1 is the same release line as 26.3.
	if !sameMCRelease("26.3", "26.3.1") {
		t.Error("patch differences must not count as a different release")
	}
	if sameMCRelease("26.2", "26.3") {
		t.Error("26.2 and 26.3 are different releases")
	}
	if sameMCRelease("1.20.1", "1.21.1") {
		t.Error("1.20.1 and 1.21.1 are different releases")
	}
}
