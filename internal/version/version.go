// Package version holds build-time metadata for modharbor.
//
// Values are overridden at link time by the release workflow, e.g.:
//
//	go build -ldflags "-X github.com/MohammadMD1383/modharbor/internal/version.Version=1.2.3"
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version of the build.
	Version = "0.1.0"
	// Commit is the git revision the binary was built from.
	Commit = "unknown"
	// Date is the RFC3339 build timestamp.
	Date = "unknown"
)

// GoVersion reports the Go runtime used for this build.
func GoVersion() string { return runtime.Version() }

// String renders the full build banner.
func String() string {
	s := Version
	if Commit != "" && Commit != "unknown" {
		s += fmt.Sprintf(" (%s)", shortCommit(Commit))
	}
	return s
}

// Banner renders the multi-line banner printed by `modharbor version`.
func Banner() string {
	out := fmt.Sprintf("modharbor %s\n", String())
	if Date != "unknown" {
		out += fmt.Sprintf("  built:    %s\n", Date)
	}
	out += fmt.Sprintf("  runtime:  %s %s/%s\n", GoVersion(), runtime.GOOS, runtime.GOARCH)
	return out
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
