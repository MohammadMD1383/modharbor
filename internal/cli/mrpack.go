package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/mrpack"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

// exportJSON is the machine-readable form of an export.
type exportJSON struct {
	Instance  string `json:"instance"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	MCVersion string `json:"mcVersion,omitempty"`
	Resolved  int    `json:"resolved"`
	Overrides int    `json:"overrides"`
	Total     int    `json:"total"`
}

// importJSON is the machine-readable form of an import.
type importJSON struct {
	Instance      string `json:"instance"`
	Pack          string `json:"pack"`
	Name          string `json:"name"`
	MCVersion     string `json:"mcVersion,omitempty"`
	DryRun        bool   `json:"dryRun"`
	Mods          int    `json:"mods"`
	Installed     int    `json:"installed"`
	Skipped       int    `json:"skipped"`
	Overrides     int    `json:"overrides"`
	WithOverrides bool   `json:"withOverrides"`
}

func newExportCmd() *cobra.Command {
	var (
		includeOverrides bool
		name             string
	)

	cmd := &cobra.Command{
		Use:   "export <dir> [instance]",
		Short: "Export an instance's mods as a Modrinth .mrpack",
		Long: strings.TrimSpace(`
Export an instance's mods as a Modrinth modpack.

Each jar is identified against Modrinth by its SHA-1, so every mod is pinned to
the exact version installed right now rather than to whatever is newest. A jar
Modrinth does not publish — a private build, a CurseForge mirror — cannot be
referenced by URL, so it is copied into the pack's overrides together with its
real sha1 and sha512. The result always reinstalls exactly what you have.

By default the instance's config, resourcepack and shaderpack directories are
packaged too, so the pack recreates a working game rather than just its mods.
`),
		Example: strings.TrimSpace(`
  # Write ./packs/26.3-fabric-mod-26.3.mrpack
  modharbor export ./packs 26.3-fabric-mod

  # Pack only the mods, leaving out configs and resource packs
  modharbor export ./packs --include-overrides=false

  # Override the pack name recorded in the manifest
  modharbor export ./packs --name "My Favourite Pack"
`),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			if flagOffline {
				// Identifying a jar is a network operation. Silently turning
				// every mod into an override would be a nasty surprise, so
				// this is refused rather than guessed at.
				return fail("export needs Modrinth to identify each jar; --offline is not supported")
			}

			inst, err := a.ResolveInstance(pickInstanceArg(args[1:]))
			if err != nil {
				return fail("%v", err)
			}

			// Overrides are staged in a scratch directory: the pack embeds
			// their bytes, so leaving them next to the output would just be
			// clutter for the user to clean up.
			overridesDir := ""
			if includeOverrides {
				overridesDir, err = stageInstanceOverrides(inst)
				if err != nil {
					return fail("%v", err)
				}
				defer os.RemoveAll(overridesDir)
			}

			res, err := mrpack.Export(cmd.Context(), a.MR(), args[0], inst.Path, inst.MCVersion, overridesDir)
			if err != nil {
				return fail("%v", err)
			}
			if name != "" {
				res.Name = name
			}

			if flagJSON {
				return printJSON(exportJSON{
					Instance: inst.ID, Path: res.Path, Name: res.Name,
					MCVersion: res.MCVersion, Resolved: res.Resolved,
					Overrides: res.Overrides, Total: res.Total,
				})
			}
			if flagQuiet {
				ui.Blank()
				ui.Success("wrote %s", res.Path)
				ui.Blank()
				return nil
			}
			renderExport(inst, res, includeOverrides)
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&includeOverrides, "include-overrides", true, "package config, resourcepack and shaderpack directories too")
	f.StringVar(&name, "name", "", "pack name recorded in the manifest")
	return cmd
}

func newImportCmd() *cobra.Command {
	var (
		yes         bool
		dryRun      bool
		noOverrides bool
	)

	cmd := &cobra.Command{
		Use:   "import <file.mrpack> [instance]",
		Short: "Install the mods from a Modrinth .mrpack",
		Long: strings.TrimSpace(`
Install the mods from a Modrinth modpack into an instance.

Files are fetched from the URLs recorded in the pack and verified against its
sha1 before they become visible to the loader. A jar whose digest is already
present is skipped, so re-running an import is harmless.

The pack's overrides/ tree — configs, resource packs, shader packs — is copied
to the instance root as a launcher would. Pass --no-overrides to install the
mods and nothing else.
`),
		Example: strings.TrimSpace(`
  modharbor import ./packs/26.3-fabric-mod-26.3.mrpack 26.3-fabric-mod

  # See what would be downloaded
  modharbor import pack.mrpack --dry-run

  # Mods only, no configs
  modharbor import pack.mrpack --no-overrides
`),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			inst, err := a.ResolveInstance(pickInstanceArg(args[1:]))
			if err != nil {
				return fail("%v", err)
			}

			pack, err := mrpack.Load(args[0])
			if err != nil {
				return fail("%v", err)
			}
			mods := pack.Mods()
			overrides := pack.Overrides()
			withOverrides := !noOverrides

			if flagJSON || dryRun {
				return renderImportPlan(inst, pack, args[0], mods, overrides, withOverrides)
			}

			if !yes && ui.IsInteractive() && !flagQuiet {
				ui.Heading("Import", inst.Label()+"  "+ui.Arrow()+"  "+ui.Path(args[0]))
				ui.Blank()
				ui.Task("add", fmt.Sprintf("%d mod(s) to install", len(mods)),
					ui.Muted(orDash(pack.Name)))
				if withOverrides && len(overrides) > 0 {
					ui.Task("add", fmt.Sprintf("%d override file(s)", len(overrides)),
						ui.Muted("into the instance root"))
				}
				ui.Blank()
				p := ui.NewPrompter(false)
				if !p.Confirm("\n  Install these?", false) {
					ui.Blank()
					ui.Info("nothing was installed")
					ui.Blank()
					return nil
				}
			}

			installed, skipped, err := mrpack.Install(cmd.Context(), a.MR(), pack,
				inst.ModsDirOrDefault(), inst.Path, withOverrides)
			if err != nil {
				return fail("%v", err)
			}

			if flagJSON {
				return printJSON(importJSON{
					Instance: inst.ID, Pack: args[0], Name: pack.Name,
					MCVersion: packMCVersion(pack), DryRun: false, Mods: len(mods),
					Installed: installed, Skipped: skipped, Overrides: len(overrides),
					WithOverrides: withOverrides,
				})
			}
			if flagQuiet {
				ui.Blank()
				ui.Success("installed %d mod(s) from %s", installed, orDash(pack.Name))
				ui.Blank()
				return nil
			}
			renderImport(inst, pack, args[0], mods, installed, skipped, overrides, withOverrides)
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	f.BoolVar(&dryRun, "dry-run", false, "list what would be installed without downloading")
	f.BoolVar(&noOverrides, "no-overrides", false, "do not copy the pack's overrides directory")
	return cmd
}

// overrideDirs are the instance directories a launcher copies verbatim when
// installing a pack. They are what --include-overrides is about.
var overrideDirs = []string{"config", "resourcepacks", "shaderpacks"}

// overrideFiles are the loose instance-root files worth carrying. Anything
// else at the root (saves, logs, launchers' own metadata) is noise.
var overrideFiles = []string{"options.txt", "servers.dat", "server.properties"}

// stageInstanceOverrides copies the instance's user-owned files into a scratch
// directory shaped like a pack's overrides/ tree, and returns its path.
//
// The caller owns the directory and is expected to delete it: Export only reads
// it, and the bytes end up inside the archive.
func stageInstanceOverrides(inst *instance.Info) (string, error) {
	dir, err := os.MkdirTemp("", "modharbor-overrides-*")
	if err != nil {
		return "", fmt.Errorf("preparing overrides: %w", err)
	}
	for _, d := range overrideDirs {
		from := inst.Dir(d)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if err := copyTree(from, filepath.Join(dir, d)); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("packaging %s: %w", d, err)
		}
	}
	for _, f := range overrideFiles {
		from := inst.Dir(f)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if err := copyFileTo(from, filepath.Join(dir, f)); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("packaging %s: %w", f, err)
		}
	}
	return dir, nil
}

// packMCVersion reads the game version out of the manifest's dependency map.
func packMCVersion(pack *mrpack.Modpack) string {
	return pack.Dependencies["minecraft"].VersionID
}

// presentDigests reports which of a pack's files are already in modsDir, so a
// plan can tell a download apart from a no-op without touching the network.
func presentDigests(modsDir string, mods []mrpack.File) map[string]bool {
	files, err := instance.ModFiles(modsDir)
	if err != nil {
		return nil
	}
	want := map[string]bool{}
	for _, f := range mods {
		if sha := f.SHA1(); sha != "" {
			want[sha] = false
		}
	}
	for _, f := range files {
		sha, err := hashutil.SHA1File(f)
		if err != nil {
			continue
		}
		if _, tracked := want[sha]; tracked {
			want[sha] = true
		}
	}
	return want
}

// renderExport prints the result of writing a pack.
func renderExport(inst *instance.Info, res mrpack.ExportResult, includeOverrides bool) {
	ui.Heading("Export", inst.Label()+"  "+ui.Arrow()+"  "+ui.Path(res.Path))
	ui.Blank()

	ui.Task("ok", truncateName(res.Name, 30), ui.Muted(orDash(res.MCVersion)))
	ui.Task("ok", fmt.Sprintf("%d mod(s)", res.Resolved), ui.OK("resolved by download URL"))
	if res.Overrides > 0 {
		ui.Task("warn", fmt.Sprintf("%d override(s)", res.Overrides),
			ui.Warnc("no upstream match — carried verbatim"))
	}
	ui.Blank()

	p := ui.NewPanel("Summary")
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(res.Resolved), 4), ui.OK("referenced by URL"))
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(res.Overrides), 4), ui.Warnc("shipped as overrides"))
	if includeOverrides {
		p.Linef("%s  %s", "    —", ui.Muted("configs and resource packs included"))
	} else {
		p.Linef("%s  %s", "    —", ui.Faint("configs and resource packs omitted"))
	}
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(res.Total), 4), ui.Faint("files in the manifest"))
	p.Render()
	ui.Blank()

	ui.Success("wrote %s", ui.Path(res.Path))
	if res.Overrides > 0 {
		ui.Note("%d mod(s) had no Modrinth match and were embedded in the pack", res.Overrides)
	}
	ui.Hint("restore it anywhere with `modharbor import %s <instance>`", ui.Path(filepath.Base(res.Path)))
	ui.Blank()
}

// renderImportPlan shows what an import would do, downloading nothing.
func renderImportPlan(inst *instance.Info, pack *mrpack.Modpack, path string, mods, overrides []mrpack.File, withOverrides bool) error {
	if flagJSON {
		return printJSON(importJSON{
			Instance: inst.ID, Pack: path, Name: pack.Name,
			MCVersion: packMCVersion(pack), DryRun: true, Mods: len(mods),
			Overrides: len(overrides), WithOverrides: withOverrides,
		})
	}

	ui.Heading("Import plan", inst.Label()+"  "+ui.Arrow()+"  "+ui.Path(path))
	ui.Blank()

	present := presentDigests(inst.ModsDirOrDefault(), mods)
	for _, f := range mods {
		state, detail := "add", ui.Muted(shortHash(f.SHA1()))
		switch {
		case f.SHA1() != "" && present[f.SHA1()]:
			state, detail = "skip", ui.Faint("already installed")
		case len(f.Downloads) == 0:
			state, detail = "warn", ui.Warnc("override — copied from the pack")
		}
		ui.Task(state, truncateName(f.Name(), 30), detail)
	}
	if len(mods) == 0 {
		ui.Task("skip", "no mods in this pack", ui.Faint("nothing to download"))
	}
	ui.Blank()

	p := ui.NewPanel("Summary")
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(mods)), 4), ui.OK("mods in the pack"))
	if withOverrides {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(overrides)), 4), ui.Muted("override files"))
	} else {
		p.Linef("%s  %s", ui.PadLeft("0", 4), ui.Faint("overrides skipped (--no-overrides)"))
	}
	p.Linef("%s  %s", ui.PadLeft(orDash(packMCVersion(pack)), 4), ui.Faint("minecraft version"))
	p.Render()
	ui.Blank()

	ui.Info("dry run: nothing was installed")
	ui.Hint("re-run without --dry-run to apply")
	ui.Blank()
	return nil
}

// renderImport prints what an install actually did.
//
// Install reports totals rather than per-file outcomes, so the view sticks to
// counts; claiming "installed" next to a specific jar it did not download
// would be worse than saying nothing.
func renderImport(inst *instance.Info, pack *mrpack.Modpack, path string, mods []mrpack.File, installed, skipped int, overrides []mrpack.File, withOverrides bool) {
	ui.Heading("Import complete", inst.Label()+"  "+ui.Arrow()+"  "+ui.Path(path))
	ui.Blank()

	ui.Task("ok", fmt.Sprintf("%d mod(s) installed", installed), ui.OK(orDash(pack.Name)))
	if skipped > 0 {
		ui.Task("skip", fmt.Sprintf("%d already present", skipped), ui.Faint("identical sha1 — not re-downloaded"))
	}
	if withOverrides && len(overrides) > 0 {
		ui.Task("add", fmt.Sprintf("%d override file(s)", len(overrides)), ui.Muted("copied to the instance root"))
	}
	if installed == 0 && skipped == 0 && len(overrides) == 0 {
		ui.Task("skip", "this pack contains no files", ui.Faint("nothing to do"))
	}
	ui.Blank()

	p := ui.NewPanel("Summary")
	p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(installed), 4), ui.OK("installed"))
	if skipped > 0 {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(skipped), 4), ui.Faint("already present"))
	}
	if withOverrides {
		p.Linef("%s  %s", ui.PadLeft(fmt.Sprint(len(overrides)), 4), ui.Muted("override files"))
	}
	p.Render()
	ui.Blank()

	ui.Success("installed %d mod(s) from %s", installed, orDash(pack.Name))
	if withOverrides && len(overrides) > 0 {
		ui.Note("%d override file(s) copied to %s", len(overrides), ui.Path(inst.Path))
	}
	ui.Hint("check the result with `modharbor doctor %s`", inst.ID)
	ui.Blank()
}

// shortHash renders a digest's leading bytes, which is enough for a human to
// eyeball and short enough not to wrap a terminal.
func shortHash(s string) string {
	if s == "" {
		return ui.Faint("no digest recorded")
	}
	if len(s) > 12 {
		s = s[:12]
	}
	return ui.Faint(s)
}
