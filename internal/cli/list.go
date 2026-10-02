package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var (
		onlyKnown bool
		force     bool
	)

	cmd := &cobra.Command{
		Use:     "list [instance]",
		Aliases: []string{"ls"},
		Short:   "List the mods installed in an instance",
		Long: strings.TrimSpace(`
List every jar in an instance's mods directory with its resolved identity.

Identities come from the local cache, so repeat runs are instant. A jar is
still listed even when nothing upstream matches it, marked with a dim dot,
because knowing what is installed matters more than knowing what it resolved
to. Pass --offline to guarantee no network requests.
`),
		Example: strings.TrimSpace(`
  modharbor list
  modharbor list 26.3-fabric-mod --all
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			inst, err := a.ResolveInstance(pickInstanceArg(args))
			if err != nil {
				return fail("%v", err)
			}

			rows, err := scanInstance(cmd.Context(), a, inst, force)
			if err != nil {
				return fail("%v", err)
			}

			if flagJSON {
				return printJSON(newScanJSON(inst, rows))
			}

			identified := 0
			for _, r := range rows {
				if r.Method != resolver.MethodUnmatched {
					identified++
				}
			}

			ui.Heading("Installed mods", inst.Label())
			ui.Blank()

			tab := ui.NewTable("", "MOD", "VERSION", "SIZE", "ID")
			tab.Align(0, ui.AlignRight)
			tab.Align(3, ui.AlignRight)

			for _, r := range rows {
				if r.Method == resolver.MethodUnmatched && onlyKnown {
					continue
				}
				marker := ui.OK(ui.SymOK)
				if r.Method == resolver.MethodUnmatched {
					marker = ui.Faint(ui.SymDot)
				}
				tab.Row(
					marker,
					truncateName(r.Title, 32),
					ui.Muted(orDash(r.Version)),
					ui.Faint(ui.HumanBytes(r.Size)),
					ui.Faint(orDash(r.ModID)),
				)
			}
			tab.Footer("", fmt.Sprint(identified), "identified", ui.HumanBytes(totalSize(rows)), "")
			tab.Render()

			if len(rows) == 0 {
				ui.Warn("this instance has no mods installed")
				ui.Blank()
				return nil
			}
			if identified < len(rows) {
				ui.Note("%d jar(s) have no upstream match; run `modharbor scan` to try again", len(rows)-identified)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&onlyKnown, "known", false, "hide mods with no upstream match")
	cmd.Flags().BoolVar(&force, "force", false, "re-resolve identities")
	return cmd
}

func totalSize(rows []ScannedMod) int64 {
	var n int64
	for _, r := range rows {
		n += r.Size
	}
	return n
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ─── instances ──────────────────────────────────────────────────────────────

func newInstancesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instances",
		Aliases: []string{"profiles"},
		Short:   "List the Minecraft instances modharbor can see",
		Long: strings.TrimSpace(`
Discover every instance under the Minecraft directory.

The Minecraft version is read from launcher metadata when available, which is
what makes migration correct for launchers that use names like "26.3-fabric-mod".
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			insts, err := a.Instances()
			if err != nil {
				return fail("%v\n\nSet it with `modharbor config set minecraftDir <path>`", err)
			}
			if flagJSON {
				type row struct {
					ID        string `json:"id"`
					MCVersion string `json:"mcVersion,omitempty"`
					Loader    string `json:"loader"`
					LoaderVer string `json:"loaderVersion,omitempty"`
					Mods      int    `json:"mods"`
					Launcher  string `json:"launcher,omitempty"`
					Path      string `json:"path"`
				}
				out := make([]row, 0, len(insts))
				for _, i := range insts {
					out = append(out, row{
						ID: i.ID, MCVersion: i.MCVersion, Loader: loaderLabel(i.Type),
						LoaderVer: i.LoaderVersion, Mods: i.ModCount,
						Launcher: i.Launcher, Path: i.Path,
					})
				}
				sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
				return printJSON(out)
			}

			ui.Heading("Instances", a.MinecraftDir())
			ui.Blank()
			if len(insts) == 0 {
				ui.Warn("no instances found under %s", a.MinecraftDir())
				return nil
			}

			tab := ui.NewTable("ID", "MC", "LOADER", "MODS", "LAUNCHER")
			tab.Align(2, ui.AlignRight)
			for _, i := range insts {
				mods := ui.Faint("0")
				if i.ModCount > 0 {
					mods = ui.Bold(fmt.Sprint(i.ModCount))
				}
				tab.Row(
					i.ID,
					ui.Muted(orDash(i.MCVersion)),
					loaderCell(i),
					mods,
					ui.Faint(orDash(i.Launcher)),
				)
			}
			tab.Render()
			return nil
		},
	}
	return cmd
}

func loaderCell(i *instance.Info) string {
	l := loaderLabel(i.Type)
	if i.LoaderVersion != "" {
		return ui.Muted(l) + ui.Faint(" "+i.LoaderVersion)
	}
	return ui.Muted(l)
}
