package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B6: the search table rendered only the first letter of each slug, so every
// row began with a single meaningless character in a column headed by nothing.
func TestSearchRendersNoSlugLetterColumn(t *testing.T) {
	fx := newFakeModrinth(t)
	mcDir, _ := newTestInstance(t, "26.3-fabric-mod", nil)
	writeTestConfig(t, mcDir, fx.API())

	const title = "Continuity"
	fx.searchHits = []map[string]any{{
		"project_id": sodiumProjectID, "slug": sodiumSlug, "title": title,
		"description": "Connected textures", "downloads": 1_234_567,
	}}

	out, err := captureCLI(t, "search", "textures")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}

	row := lineWith(out, title)
	if row == "" {
		t.Fatalf("no row for %q in output:\n%s", title, out)
	}
	// The row must begin with the title itself. Before the fix the first cell
	// was the slug's initial, so the line started with a single letter.
	if got := strings.TrimSpace(row); !strings.HasPrefix(got, title) {
		t.Errorf("row starts with %q, want the project title %q\nline: %q", got, title, row)
	}
}

// B5: the README documented `modharbor list --all`, which is not a flag the
// command has — it is `--known`, with the opposite meaning. There is no --all
// alias because `list` already shows every jar and --known is the useful
// filter. This pins the documentation to the real flag.
func TestReadmeDoesNotDocumentListAll(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}

	doc := string(readme)
	for _, line := range strings.Split(doc, "\n") {
		// Only invocations count. Prose such as "add <slug> --all" is
		// describing a different command entirely.
		if !strings.Contains(line, "list") || !strings.Contains(line, "--all") {
			continue
		}
		if strings.Contains(line, "config list") || strings.Contains(line, "rollback") {
			continue
		}
		t.Errorf("README documents a `list` invocation with --all: %q", strings.TrimSpace(line))
	}

	mustContain(t, doc, "`--known` hides unmatched")
}
