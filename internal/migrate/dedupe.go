package migrate

import (
	"fmt"
	"strings"
)

// dedupeByProject collapses results that target the same project.
//
// A very common real-world case is two copies of Fabric API in one mods
// folder (a stale 26.2 build plus a newer one). Both resolve to the same
// upstream project and would be written to the same destination file name,
// so we keep the newest source version and report the rest as duplicates.
//
// The duplicates are still surfaced, never silently dropped, because a
// duplicate usually means the instance has a problem worth fixing.
func dedupeByProject(results []Result) []Result {
	// Group indices by project id (or file name for copy actions).
	groups := map[string][]int{}
	for i, r := range results {
		key := dedupeKey(r)
		groups[key] = append(groups[key], i)
	}

	var drop = map[int]Result{}
	for _, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		winner := pickWinner(results, idx)

		for _, i := range idx {
			if i == winner {
				continue
			}
			r := results[i]
			r.Action = ActionDuplicate
			r.Reason = fmt.Sprintf("duplicate of %s (%s)", results[winner].Title, orUnknown(winnerVersion(results[winner])))
			if r.TargetFile != "" && results[winner].TargetFile != "" {
				r.Reason = fmt.Sprintf("same mod as %s; would overwrite %s", results[winner].Title, results[winner].TargetFile)
			}
			drop[i] = r
		}
	}

	if len(drop) == 0 {
		return results
	}

	out := make([]Result, 0, len(results))
	for i, r := range results {
		if d, ok := drop[i]; ok {
			out = append(out, d)
			continue
		}
		out = append(out, r)
	}
	return out
}

// pickWinner chooses which of several same-project results survives.
//
// Preference order (see compareFreshness):
//  1. the newest source version, since that is what the user last installed
//  2. the longer destination file name, which usually reflects the more
//     complete build
//  3. the source file name, purely so the choice is stable
func pickWinner(results []Result, idx []int) int {
	best := idx[0]
	for _, i := range idx[1:] {
		if compareFreshness(results[i], results[best]) > 0 {
			best = i
		}
	}
	return best
}

// compareFreshness compares two results of the same project.
func compareFreshness(a, b Result) int {
	c := compareVersionStrings(a.SourceVersion, b.SourceVersion)
	if c != 0 {
		return c
	}
	// Equal versions: prefer the longer file name, which usually reflects the
	// more complete build (e.g. a version that includes libraries).
	if len(a.TargetFile) > len(b.TargetFile) {
		return 1
	}
	if len(a.TargetFile) < len(b.TargetFile) {
		return -1
	}
	return strings.Compare(a.SourceFile, b.SourceFile)
}

// compareVersionStrings compares dotted-numeric version strings numerically.
// Non-numeric components fall back to a plain string compare so that mixed
// versions still order deterministically.
func compareVersionStrings(a, b string) int {
	an, aok := parseNumericVersion(a)
	bn, bok := parseNumericVersion(b)
	switch {
	case aok && bok:
		for i := 0; i < len(an) || i < len(bn); i++ {
			var x, y int
			if i < len(an) {
				x = an[i]
			}
			if i < len(bn) {
				y = bn[i]
			}
			if x != y {
				if x > y {
					return 1
				}
				return -1
			}
		}
		return strings.Compare(a, b)
	case aok:
		return 1
	case bok:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

// parseNumericVersion extracts the leading dotted-numeric run of a version
// string, e.g. "0.154.2+26.2" -> [0 154 2].
func parseNumericVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	// Skip a leading "v".
	v = strings.TrimPrefix(v, "v")

	var out []int
	cur := ""
	seenDigit := false
	for _, r := range v {
		if r >= '0' && r <= '9' {
			cur += string(r)
			seenDigit = true
			continue
		}
		if r == '.' && seenDigit {
			out = append(out, atoiOrZero(cur))
			cur = ""
			seenDigit = false
			continue
		}
		break
	}
	if seenDigit && cur != "" {
		out = append(out, atoiOrZero(cur))
	}
	if len(out) == 0 {
		return nil, false
	}
	// A bare year-like number is not a version.
	if len(out) == 1 {
		return nil, false
	}
	return out, true
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return n
		}
	}
	return n
}

// dedupeKey identifies the grouping unit for de-duplication.
//
// An identified mod groups by its upstream project, so two builds of the same
// project collapse. Anything else groups by source file name, which means two
// distinct private jars are never mistaken for one copy of a single mod.
func dedupeKey(r Result) string {
	if r.ProjectID != "" {
		return "project:" + r.ProjectID
	}
	return "file:" + r.SourceFile
}

func winnerVersion(r Result) string {
	if r.SourceVersion != "" {
		return r.SourceVersion
	}
	return r.TargetVersion
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown version"
	}
	return s
}
