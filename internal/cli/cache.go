package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/config"
	"github.com/MohammadMD1383/modharbor/internal/store"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cache",
		Aliases: []string{"state"},
		Short:   "Inspect and clear modharbor's local cache",
		Long: strings.TrimSpace(`
Modharbor remembers what each mod is, keyed by file hash, so that repeat
commands are instant and work offline.

  modharbor cache info     show cache size and entry counts
  modharbor cache path     print the cache file location
  modharbor cache clear    remove every cached resolution
  modharbor cache prune    drop only expired API responses
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		cacheSubCmd("info", "Show cache statistics", func(a *app.App, path string) error {
			st, err := openStore(path)
			if err != nil {
				return err
			}
			stats := st.Stats()
			if flagJSON {
				return printJSON(stats)
			}
			ui.Heading("Cache", path)
			ui.Blank()
			l := ui.NewList()
			l.Add("File", ui.Muted(path))
			l.Add("Size on disk", ui.Bold(ui.HumanBytes(stats.SizeBytes)))
			l.Add("Mod identities", ui.Bold(fmt.Sprint(stats.Resolutions)))
			l.Add("API responses", ui.Bold(fmt.Sprint(stats.CacheEntries)))
			l.Add("Instances seen", ui.Bold(fmt.Sprint(stats.Instances)))
			if !stats.UpdatedAt.IsZero() {
				l.Add("Last written", ui.Muted(stats.UpdatedAt.Format("2006-01-02 15:04:05")))
			}
			l.Render()
			ui.Blank()
			return nil
		}),
		cacheSubCmd("path", "Print the cache file path", func(a *app.App, path string) error {
			fmt.Println(path)
			return nil
		}),
		cacheSubCmd("clear", "Remove all cached data", func(a *app.App, path string) error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fail("%v", err)
			}
			if flagJSON {
				return printJSON(map[string]any{"cleared": true})
			}
			ui.Success("cache cleared")
			return nil
		}),
		cacheSubCmd("prune", "Drop expired API responses", func(a *app.App, path string) error {
			st, err := openStore(path)
			if err != nil {
				return err
			}
			n := st.PruneExpired()
			if err := st.Save(); err != nil {
				return fail("%v", err)
			}
			if flagJSON {
				return printJSON(map[string]any{"pruned": n})
			}
			ui.Success("pruned %d expired %s", n, plural(n, "entry", "entries"))
			return nil
		}),
		cacheSubCmd("forget <sha1>", "Drop one cached identity", func(a *app.App, path string) error {
			sha := cacheSubArg
			st, err := openStore(path)
			if err != nil {
				return err
			}
			if _, ok := st.Lookup(sha); !ok {
				return fail("no cached entry for %s", sha)
			}
			st.Forget(sha)
			if err := st.Save(); err != nil {
				return fail("%v", err)
			}
			ui.Success("forgot %s", ui.Bold(shortSHA(sha)))
			return nil
		}),
	)
	return cmd
}

// cacheSubArg holds the positional argument for single-argument subcommands.
var cacheSubArg string

func cacheSubCmd(name, short string, run func(a *app.App, path string) error) *cobra.Command {
	c := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			return run(a, a.Paths.StateFile)
		},
	}
	if strings.Contains(name, " ") {
		arg := strings.Fields(name)[1]
		c.Use = arg
		c.Args = cobra.ExactArgs(1)
		c.RunE = func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			cacheSubArg = args[0]
			return run(a, a.Paths.StateFile)
		}
	}
	return c
}

func openStore(path string) (*store.Store, error) {
	st, err := store.Open(path)
	if err != nil {
		return nil, fail("%v", err)
	}
	return st, nil
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ─── config ─────────────────────────────────────────────────────────────────

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and change modharbor's settings",
		Long: strings.TrimSpace(`
View or change modharbor's configuration.

  modharbor config list
  modharbor config get minecraftDir
  modharbor config set defaultInstance 26.3-fabric-mod
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigList(cmd)
		},
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "Show the current configuration",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return runConfigList(cmd) },
		},
		&cobra.Command{
			Use:   "get <key>",
			Short: "Print one setting",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := bootstrap()
				if err != nil {
					return fail("%v", err)
				}
				v, ok := configLookup(a, args[0])
				if !ok {
					return fail("unknown setting %q; run `modharbor config list`", args[0])
				}
				fmt.Println(v)
				return nil
			},
		},
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Change one setting",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := bootstrap()
				if err != nil {
					return fail("%v", err)
				}
				if err := configSet(a, args[0], args[1]); err != nil {
					return fail("%v", err)
				}
				if flagJSON {
					return printJSON(map[string]string{args[0]: args[1]})
				}
				ui.Success("%s = %s", ui.Bold(args[0]), ui.OK(args[1]))
				return nil
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				a, err := bootstrap()
				if err != nil {
					return fail("%v", err)
				}
				fmt.Println(a.Paths.ConfigFile)
				return nil
			},
		},
	)
	return cmd
}

func runConfigList(cmd *cobra.Command) error {
	a, err := bootstrap()
	if err != nil {
		return fail("%v", err)
	}
	if flagJSON {
		return printJSON(map[string]any{
			"file":            a.Paths.ConfigFile,
			"minecraftDir":    a.MinecraftDir(),
			"defaultInstance": a.Config.DefaultInstance,
			"channel":         a.Config.Update.Channel,
			"concurrency":     a.Concurrency(),
			"modrinth":        map[string]any{"baseUrl": a.Config.Modrinth.BaseURL, "apiKey": boolSet(a.Config.Modrinth.APIKey)},
			"curseForge":      map[string]any{"baseUrl": a.Config.CurseForge.BaseURL, "apiKey": boolSet(a.Config.CurseForge.APIKey)},
			"verifyDownloads": a.Config.Update.VerifyDownloads,
			"autoBackup":      a.Config.Update.AutoBackup,
		})
	}

	ui.Heading("Configuration", a.Paths.ConfigFile)
	ui.Blank()
	l := ui.NewList()
	l.Add("minecraftDir", ui.Path(a.MinecraftDir()))
	l.Add("defaultInstance", valueOrDash(a.Config.DefaultInstance))
	l.Add("channel", ui.Muted(a.Config.Update.Channel))
	l.Add("concurrency", ui.Muted(fmt.Sprint(a.Concurrency())))
	l.Add("modrinth.url", ui.Muted(a.Config.Modrinth.BaseURL))
	l.Add("modrinth.apiKey", secretState(a.Config.Modrinth.APIKey))
	l.Add("curseForge.apiKey", secretState(a.Config.CurseForge.APIKey))
	l.Add("verifyDownloads", ui.Muted(fmt.Sprint(a.Config.Update.VerifyDownloads)))
	l.Add("autoBackup", ui.Muted(fmt.Sprint(a.Config.Update.AutoBackup)))
	l.Render()
	ui.Blank()
	ui.Hint("change one with `modharbor config set <key> <value>`")
	ui.Blank()
	return nil
}

func boolSet(s string) bool { return strings.TrimSpace(s) != "" }

func secretState(s string) string {
	if !boolSet(s) {
		return ui.Faint("not set")
	}
	return ui.OK("set")
}

func valueOrDash(s string) string {
	if s == "" {
		return ui.Faint("—")
	}
	return ui.Bold(s)
}

func configLookup(a *app.App, key string) (string, bool) {
	c := a.Config
	switch strings.ToLower(key) {
	case "minecrafdir", "minecraftdir":
		return c.MinecraftDirectory(), true
	case "defaultinstance":
		return c.DefaultInstance, true
	case "channel":
		return c.Update.Channel, true
	case "concurrency":
		return fmt.Sprint(a.Concurrency()), true
	case "modrinth.apikey":
		return c.Modrinth.APIKey, true
	case "modrinth.baseurl":
		return c.Modrinth.BaseURL, true
	case "curseforge.apikey":
		return c.CurseForge.APIKey, true
	case "verifydownloads":
		return fmt.Sprint(c.Update.VerifyDownloads), true
	case "autobackup":
		return fmt.Sprint(c.Update.AutoBackup), true
	}
	return "", false
}

// configSet applies one setting and persists the config file.
func configSet(a *app.App, key, value string) error {
	c := a.Config
	switch strings.ToLower(key) {
	case "minecrafdir", "minecraftdir":
		if value == "" {
			c.MinecraftDir = ""
		} else {
			abs, err := filepath.Abs(expandHome(value))
			if err != nil {
				return err
			}
			c.MinecraftDir = abs
		}
	case "defaultinstance":
		c.DefaultInstance = value
	case "channel":
		switch value {
		case "release", "beta", "alpha":
			c.Update.Channel = value
		default:
			return fmt.Errorf("channel must be release, beta or alpha")
		}
	case "concurrency":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 16 {
			return fmt.Errorf("concurrency must be between 1 and 16")
		}
		c.Update.Concurrency = n
	case "modrinth.apikey":
		c.Modrinth.APIKey = value
	case "curseforge.apikey":
		c.CurseForge.APIKey = value
	case "verifydownloads":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.Update.VerifyDownloads = b
	case "autobackup":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.Update.AutoBackup = b
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	return configSave(a)
}

// configSave writes the config file back to disk.
func configSave(a *app.App) error {
	if err := os.MkdirAll(a.Paths.ConfigDir, 0o755); err != nil {
		return err
	}
	return config.Save(a.Paths, a.Config)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
