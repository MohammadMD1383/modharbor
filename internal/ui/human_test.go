package ui

import (
	"testing"
)

// HumanBytes is used for every size the CLI shows, so its boundaries decide
// whether a mod reads as "512 B" or "0.5 kB". Getting the unit wrong by one
// step is not a cosmetic slip: it is how a 1.4 GB modset reads as "1400.0 MB".
func TestHumanBytesSwitchesUnitsAtTheThousandBoundary(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"one byte", 1, "1 B"},
		// The last value still rendered in bytes. 1000 is the first kB, not
		// 1024 — these are SI units, and mixing the two is a real risk here
		// because the binary units are the more familiar ones.
		{"just under a kilobyte", 999, "999 B"},
		{"exactly a kilobyte", 1000, "1.0 kB"},
		{"mid kilobyte", 1500, "1.5 kB"},
		{"just under a megabyte", 999_999, "1000.0 kB"},
		{"exactly a megabyte", 1_000_000, "1.0 MB"},
		{"mid megabyte", 13_500_000, "13.5 MB"},
		{"exactly a gigabyte", 1_000_000_000, "1.0 GB"},
		{"exactly a terabyte", 1_000_000_000_000, "1.0 TB"},
		// Rounding happens after the unit is chosen, so the largest value in
		// a unit can display as "1000.0" of that same unit rather than
		// promoting itself to the next one.
		{"just under a petabyte", 999_999_999_999_999, "1000.0 TB"},
		{"exactly a petabyte", 1_000_000_000_000_000, "1.0 PB"},
		// A negative size is not expected, but it must not read as a kB count.
		{"negative", -1, "-1 B"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HumanBytes(tc.in); got != tc.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// HumanCount appears next to download totals, so the separator has to be the
// conventional comma at every three digits. An off-by-one here shows up only in
// the four-digit range, which is why 999 and 1000 are both pinned.
func TestHumanCountGroupsDigitsInThrees(t *testing.T) {
	cases := map[int]string{
		0:           "0",
		7:           "7",
		999:         "999",
		1_000:       "1,000",
		1_001:       "1,001",
		12_345:      "12,345",
		999_999:     "999,999",
		1_000_000:   "1,000,000",
		123_456_789: "123,456,789",
		-1_234:      "-1,234",
	}
	for in, want := range cases {
		if got := HumanCount(in); got != want {
			t.Errorf("HumanCount(%d) = %q, want %q", in, got, want)
		}
	}
}

// HumanDuration labels how long a download has been running. Sub-second and
// multi-hour values take different branches, and the zero case is the one that
// shows up while a spinner is still warming up.
func TestHumanDurationPicksABranchThatMatchesTheMagnitude(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, "0ms"},
		{"sub second", 0.25, "250ms"},
		{"just under a second", 0.999, "999ms"},
		{"exactly a second", 1, "1.0s"},
		{"seconds", 12.34, "12.3s"},
		{"just under a minute", 59.9, "59.9s"},
		// Seconds are zero padded from here on: the column has to stay
		// readable as a clock while a download progresses.
		{"exactly a minute", 60, "1m 00s"},
		{"minutes and seconds", 90, "1m 30s"},
		{"just under an hour", 3599, "59m 59s"},
		{"exactly an hour", 3600, "1h 00m"},
		{"hours and minutes", 3661, "1h 01m"},
		{"many hours", 36000, "10h 00m"},
		// Sub-millisecond progress rounds down to nothing; it must not read
		// as a negative or as a blank cell.
		{"under a millisecond", 0.0004, "0ms"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HumanDuration(tc.in); got != tc.want {
				t.Errorf("HumanDuration(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
