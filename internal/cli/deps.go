package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newDepsCmd() *cobra.Command {
	var missing bool

	cmd := &cobra.Command{
		Use:   "deps [instance]",
		Short: "Show what each mod depends on",
		Long: strings.TrimSpace(`
List the dependencies declared by every installed mod, and which are satisfied.

This reads each jar's own metadata (fabric.mod.json, quilt.mod.json or
mods.toml), so it works offline and needs no network lookup.
`),
		Example: strings.TrimSpace(`
  modharbor deps
  modharbor deps --missing
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			inst, err := resolveInstance(a, args)
			if err != nil {
				return err
			}
			rows, err := scanInstance(cmd.Context(), a, inst, false)
			if err != nil {
				return fail("%v", err)
			}

			deps := collectDeps(inst, rows)
			present := presentIDs(inst, rows)

			if flagJSON {
				for i := range deps {
					deps[i].Satisfied = present[strings.ToLower(deps[i].Dep)]
				}
				return printJSON(deps)
			}

			ui.Heading("Dependencies", inst.Label())
			ui.Blank()

			if missing {
				shown := 0
				for _, d := range deps {
					if present[strings.ToLower(d.Dep)] {
						continue
					}
					shown++
					ui.Task("warn", ui.Pad(ui.Truncate(d.Mod, 28), 30),
						ui.Bad("needs "+d.Dep)+ui.Faint("  "+d.Range))
				}
				ui.Blank()
				if shown == 0 {
					ui.Success("all declared dependencies are satisfied")
				} else {
					ui.Warn("%d missing dependency reference(s)", shown)
				}
				ui.Blank()
				return nil
			}

			if len(deps) == 0 {
				ui.Warn("no dependencies declared")
				ui.Blank()
				return nil
			}

			tab := ui.NewTable("", "MOD", "REQUIRES", "RANGE")
			tab.Align(0, ui.AlignRight)
			for _, d := range deps {
				mark := ui.OK(ui.SymOK)
				if !present[strings.ToLower(d.Dep)] {
					mark = ui.Bad(ui.SymFail)
				}
				tab.Row(mark, ui.Pad(ui.Truncate(d.Mod, 28), 28), ui.Muted(d.Dep), ui.Faint(orDash(d.Range)))
			}
			tab.Render()
			ui.Hint("run `modharbor doctor` for a full compatibility report")
			ui.Blank()
			return nil
		},
	}

	cmd.Flags().BoolVar(&missing, "missing", false, "only show unsatisfied dependencies")
	return cmd
}

// loaderProvided lists mod ids the loader satisfies without a jar in mods/.
var loaderProvided = []string{
	"minecraft", "java", "fabricloader", "fabric-api", "quilt_loader",
	"forge", "neoforge", "minecraftcore", "intermediary", "kotlin-stdlib",
}

// presentIDs builds the set of mod ids a dependency check should treat as
// available: every installed jar's own id, plus the ids of the libraries
// nested inside those jars, plus what the loader itself provides.
//
// Counting nested jars matters. Sodium bundles several Fabric API modules
// under "jars/", Mod Menu bundles the screen and key-mapping APIs, and so on.
// Ignoring that produces a long list of false "missing dependency" reports.
func presentIDs(inst *instance.Info, rows []ScannedMod) map[string]bool {
	out := map[string]bool{}
	modsDir := inst.ModsDirOrDefault()
	for _, r := range rows {
		if r.ModID != "" {
			out[strings.ToLower(r.ModID)] = true
		}
		for id := range modmeta.ProvidedIDs(filepath.Join(modsDir, r.FileName)) {
			out[id] = true
		}
	}
	for _, k := range loaderProvided {
		out[k] = true
	}
	return out
}

// depRow is one declared dependency edge.
type depRow struct {
	Mod       string `json:"mod"`
	Dep       string `json:"dependency"`
	Range     string `json:"range,omitempty"`
	Satisfied bool   `json:"satisfied"`
}

// collectDeps reads each jar's own metadata and returns declared
// dependencies, sorted for stable output.
func collectDeps(inst *instance.Info, rows []ScannedMod) []depRow {
	modsDir := inst.ModsDirOrDefault()
	var out []depRow

	for _, r := range rows {
		meta, err := modmeta.Read(filepath.Join(modsDir, r.FileName))
		if err != nil || meta == nil {
			continue
		}
		for id, spec := range meta.DependsOn {
			out = append(out, depRow{Mod: r.Title, Dep: id, Range: spec})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Mod != out[j].Mod {
			return strings.ToLower(out[i].Mod) < strings.ToLower(out[j].Mod)
		}
		return out[i].Dep < out[j].Dep
	})
	return out
}
