// Package cli implements modharbor's command line interface.
//
// Every command follows the same shape: parse flags, resolve the instance,
// delegate to the engine, then render results through internal/ui.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/config"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/MohammadMD1383/modharbor/internal/version"
	"github.com/spf13/cobra"
)

// Global flags shared by every command.
var (
	flagConfig      string
	flagMinecraft   string
	flagInstance    string
	flagJSON        bool
	flagNoColour    bool
	flagForceColour bool
	flagQuiet       bool
	flagVerbose     bool
	flagOffline     bool
	flagChannel     string
)

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		return 0
	}

	// A silentError carries an exit code and may have nothing to say, which
	// is how a command reports a non-zero status without an error message.
	// Only a nil inner error is silent; one carrying text is still printed.
	var se *silentError
	if errors.As(err, &se) && se.Err == nil {
		return se.Code
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s %s\n", ui.Paint(ui.Palette.Err, ui.SymFail), ui.Bold(err.Error()))
	fmt.Fprintln(os.Stderr)

	var me *missingInstanceError
	if errors.As(err, &me) {
		fmt.Fprintf(os.Stderr, "  %s\n\n", ui.Muted("try: modharbor instances"))
	}
	return exitCodeFor(err)
}

// exitCodeFor maps an error onto a process exit status.
func exitCodeFor(err error) int {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, context.Canceled) {
		return 1
	}
	return 1
}

// silentError carries an exit code without printing anything extra.
type silentError struct {
	Code int
	Err  error
}

func (e *silentError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *silentError) Unwrap() error { return e.Err }

// newRootCmd assembles the command tree.
func newRootCmd() *cobra.Command {
	var (
		colourSet bool
		colourOn  bool
	)

	root := &cobra.Command{
		Use:   "modharbor",
		Short: "Keep your Minecraft mods working across game versions",
		Long: strings.TrimSpace(`
modharbor finds, updates and migrates Minecraft mods.

It identifies each installed jar by content hash against Modrinth, finds the
newest build compatible with your Minecraft version and loader, verifies the
download, and replaces the old jar safely.

Start here:

  modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --dry-run
  modharbor outdated
  modharbor update
`),
		SilenceUsage: true,
		// Errors are rendered by Execute so that exit codes, styling and the
		// "did you mean" hints are all in one place.
		SilenceErrors: true,
		Version:       version.String(),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Colour flags are tri-state: unset means auto-detect.
			if !colourSet {
				switch {
				case flagNoColour:
					ui.SetColorEnabled(false)
				case flagForceColour:
					ui.SetColorEnabled(true)
				default:
					ui.InitColor(flagQuiet, false)
				}
			} else {
				ui.SetColorEnabled(colourOn)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagConfig, "config", "", "path to the config file")
	pf.StringVar(&flagMinecraft, "minecraft", "", "Minecraft directory (default: auto-detect)")
	pf.StringVarP(&flagInstance, "instance", "i", "", "instance id or path (default: config value)")
	pf.BoolVar(&flagJSON, "json", false, "emit machine-readable JSON")
	pf.BoolVar(&flagNoColour, "no-color", false, "disable coloured output")
	pf.BoolVar(&flagForceColour, "color", false, "force coloured output even when piped")
	pf.BoolVarP(&flagQuiet, "quiet", "q", false, "suppress decorative output")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "report how each mod was resolved (stderr)")
	pf.BoolVar(&flagOffline, "offline", false, "use cached data only; make no network requests")
	pf.StringVar(&flagChannel, "channel", "", "release channel: release, beta or alpha")

	// Cobra's own --version flag needs a nicer presentation.
	root.SetVersionTemplate("modharbor " + version.String() + "\n")
	root.Flags().BoolP("version", "V", false, "print version information")

	root.AddCommand(
		newScanCmd(),
		newListCmd(),
		newOutdatedCmd(),
		newUpdateCmd(),
		newMigrateCmd(),
		newAddCmd(),
		newRemoveCmd(),
		newInfoCmd(),
		newSearchCmd(),
		newLinkCmd(),
		newUnlinkCmd(),
		newRollbackCmd(),
		newExportCmd(),
		newImportCmd(),
		newDoctorCmd(),
		newDepsCmd(),
		newCacheCmd(),
		newInstancesCmd(),
		newConfigCmd(),
		newCompletionCmd(),
		newVersionCmd(),
	)
	return root
}

// bootstrap loads config and builds the App, honouring global flags.
func bootstrap() (*app.App, error) {
	cfg, paths, err := config.Load(flagConfig)
	if err != nil {
		return nil, err
	}
	if flagMinecraft != "" {
		cfg.MinecraftDir = flagMinecraft
	}
	if flagChannel != "" {
		cfg.Update.Channel = config.NormalizeChannel(flagChannel)
	}
	cfg.Merge(config.Default())
	return app.New(cfg, paths), nil
}

// exitWithCode aborts with a specific code without printing a Cobra error.
func exitWithCode(code int, err error) error {
	return &silentError{Code: code, Err: err}
}

// fail returns a formatted error. Execute renders it and exits non-zero.
func fail(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}

// verbosef writes one diagnostic line to stderr when --verbose is set, and
// nothing otherwise.
//
// Diagnostics go to stderr so that --json output stays parseable and a
// redirected stdout carries only the result. The line is colour-aware and
// respects NO_COLOR, because ui.Faint falls back to plain text.
func verbosef(format string, a ...any) {
	if !flagVerbose {
		return
	}
	fmt.Fprintf(os.Stderr, "  %s %s\n", ui.Faint(ui.SymDot), fmt.Sprintf(format, a...))
}

// printJSON writes a value as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
