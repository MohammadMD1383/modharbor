package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newSearchCmd() *cobra.Command {
	var (
		limit    int
		loader   string
		sortBy   string
		category string
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search Modrinth",
		Long: strings.TrimSpace(`
Search the Modrinth catalogue.

Filters for the current instance's Minecraft version and loader are applied by
default when an instance is in scope, so results are installable as-is.
`),
		Example: strings.TrimSpace(`
  modharbor search performance
  modharbor search hud --instance 26.3-fabric-mod
  modharbor search shaders --loader fabric --sort downloads
`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}

			opts := modrinth.SearchOptions{Limit: limit}
			if loader != "" {
				opts.Loader = loader
			}
			if category != "" {
				opts.Categories = []string{category}
			}
			switch sortBy {
			case "downloads", "download":
				opts.Sorts = []string{"downloads"}
			case "follows", "popularity":
				opts.Sorts = []string{"follows"}
			case "newest", "updated":
				opts.Sorts = []string{"updated"}
			case "", "relevance":
				// API default.
			default:
				return fail("unknown sort %q; use relevance, downloads, follows or newest", sortBy)
			}

			// When an instance is in scope, filter results to versions that
			// are actually installable there. Failing to resolve one is fine:
			// search should still work without a Minecraft directory, so this
			// goes through the same resolution path as every other command and
			// discards the error rather than reporting a message it will not act on.
			var mcVersion string
			if inst, err := resolveInstance(a, nil); err == nil {
				mcVersion = inst.MCVersion
				if opts.Loader == "" {
					opts.Loader = loaderName(inst.Type)
				}
			}
			opts.GameVersion = mcVersion

			res, err := a.MR().Search(cmd.Context(), strings.Join(args, " "), opts)
			if err != nil {
				return fail("%v", err)
			}
			if flagJSON {
				return printJSON(res)
			}
			if len(res.Hits) == 0 {
				ui.Warn("no results")
				ui.Blank()
				return nil
			}

			subtitle := strings.Join(args, " ")
			if mcVersion != "" {
				subtitle += "  ·  MC " + mcVersion
			}
			ui.Heading("Modrinth search", subtitle)
			ui.Blank()

			// No slug column: rendering only its first letter was a leftover,
			// and produced a column of meaningless single characters. The title
			// identifies a project well enough, and `modharbor info` prints the
			// slug when it is actually needed.
			tab := ui.NewTable("PROJECT", "DESCRIPTION", "DL", "★")
			tab.Align(2, ui.AlignRight)
			tab.Align(3, ui.AlignRight)

			for _, h := range res.Hits {
				tab.Row(
					ui.Pad(h.Title, 22),
					ui.Muted(ui.Truncate(h.Description, 52)),
					compactCount(h.Downloads),
					compactCount(h.Follows),
				)
			}
			tab.Footer(fmt.Sprint(res.TotalHits), "results", "", "")
			tab.Render()
			ui.Hint("install with `modharbor add <slug>`")
			ui.Blank()
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVarP(&limit, "limit", "n", 20, "maximum results")
	f.StringVar(&loader, "loader", "", "filter by loader: fabric, forge, neoforge, quilt")
	f.StringVarP(&sortBy, "sort", "s", "relevance", "sort: relevance, downloads, follows, newest")
	f.StringVar(&category, "category", "", "filter by category, e.g. optimization or hud")
	return cmd
}

// compactCount renders large numbers as 1.2M / 45k.
func compactCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprint(n)
	}
}

var _ = sort.Strings
