// Package config loads and persists modharbor settings.
//
// Config lives at $XDG_CONFIG_HOME/modharbor/config.json (or the --config
// path). Secrets such as API keys are stored with 0600 permissions.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is the persisted configuration document.
type Config struct {
	// MinecraftDir overrides auto-detection of the .minecraft directory.
	MinecraftDir string `json:"minecraftDir,omitempty"`

	// DefaultInstance is used when no instance is given on the command line.
	DefaultInstance string `json:"defaultInstance,omitempty"`

	// CacheDir holds the content-addressed blob cache.
	CacheDir string `json:"cacheDir,omitempty"`

	// Modrinth holds Modrinth API settings.
	Modrinth ProviderConfig `json:"modrinth"`

	// CurseForge holds CurseForge API settings.
	CurseForge ProviderConfig `json:"curseForge"`

	// Update holds update-checking behaviour.
	Update UpdateConfig `json:"update"`

	// Display holds output preferences.
	Display DisplayConfig `json:"display"`
}

// ProviderConfig configures one upstream API.
type ProviderConfig struct {
	// Enabled toggles the provider.
	Enabled bool `json:"enabled"`
	// APIKey is the secret token, stored separately with restricted perms.
	APIKey string `json:"apiKey,omitempty"`
	// BaseURL overrides the API root (used for testing and mirrors).
	BaseURL string `json:"baseUrl,omitempty"`
}

// UpdateConfig controls update selection.
type UpdateConfig struct {
	// Channel is "release", "beta" or "alpha".
	Channel string `json:"channel,omitempty"`
	// IncludeSnapshots allows -snapshot Minecraft and loader versions.
	IncludeSnapshots bool `json:"includeSnapshots"`
	// Concurrency caps parallel API requests and downloads.
	Concurrency int `json:"concurrency,omitempty"`
	// HardDelete removes old jars instead of moving them to the backup dir.
	HardDelete bool `json:"hardDelete"`
	// VerifyDownloads re-hashes every download before installing.
	VerifyDownloads bool `json:"verifyDownloads"`
	// AutoBackup keeps a restorable snapshot before each change.
	AutoBackup bool `json:"autoBackup"`
}

// DisplayConfig controls terminal output.
type DisplayConfig struct {
	// NoColour disables ANSI colour.
	NoColour bool `json:"noColour"`
	// ForceColour enables ANSI colour even when piped.
	ForceColour bool `json:"forceColour"`
	// JSON emits machine-readable output for the given commands.
	JSON bool `json:"json"`
}

// Default returns a Config populated with sensible defaults.
func Default() *Config {
	return &Config{
		Modrinth: ProviderConfig{
			Enabled: true,
			BaseURL: "https://api.modrinth.com/v2",
		},
		CurseForge: ProviderConfig{
			Enabled: true,
			BaseURL: "https://api.curseforge.com/v1",
		},
		Update: UpdateConfig{
			Channel:         "release",
			Concurrency:     6,
			VerifyDownloads: true,
			AutoBackup:      true,
		},
	}
}

// Merge fills zero-valued fields of c with defaults.
func (c *Config) Merge(def *Config) {
	if c.MinecraftDir == "" {
		c.MinecraftDir = def.MinecraftDir
	}
	if c.DefaultInstance == "" {
		c.DefaultInstance = def.DefaultInstance
	}
	if c.CacheDir == "" {
		c.CacheDir = def.CacheDir
	}
	if c.Update.Channel == "" {
		c.Update.Channel = def.Update.Channel
	}
	if c.Update.Concurrency <= 0 {
		c.Update.Concurrency = def.Update.Concurrency
	}
	if c.Modrinth.BaseURL == "" {
		c.Modrinth.BaseURL = def.Modrinth.BaseURL
	}
	if c.CurseForge.BaseURL == "" {
		c.CurseForge.BaseURL = def.CurseForge.BaseURL
	}
}

// Paths resolves all filesystem locations used by modharbor.
type Paths struct {
	ConfigDir  string
	ConfigFile string
	DataDir    string
	CacheDir   string
	StateFile  string
}

// DefaultPaths derives XDG paths for the current platform.
func DefaultPaths() (Paths, error) {
	var p Paths

	if dir, err := os.UserConfigDir(); err == nil {
		p.ConfigDir = filepath.Join(dir, "modharbor")
	} else {
		home, _ := os.UserHomeDir()
		p.ConfigDir = filepath.Join(home, ".config", "modharbor")
	}
	p.ConfigFile = filepath.Join(p.ConfigDir, "config.json")

	if dir, err := os.UserCacheDir(); err == nil {
		p.CacheDir = filepath.Join(dir, "modharbor")
	} else {
		home, _ := os.UserHomeDir()
		p.CacheDir = filepath.Join(home, ".cache", "modharbor")
	}

	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		p.DataDir = filepath.Join(home, "AppData", "Local", "modharbor")
	} else if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		p.DataDir = filepath.Join(dir, "modharbor")
	} else {
		p.DataDir = filepath.Join(home, ".local", "share", "modharbor")
	}
	p.StateFile = filepath.Join(p.DataDir, "state.json")

	return p, nil
}

// Load reads the config file, creating it with defaults when absent.
func Load(path string) (*Config, Paths, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, paths, err
	}
	if path != "" {
		paths.ConfigFile = path
	}

	cfg := Default()
	if data, err := os.ReadFile(paths.ConfigFile); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, paths, fmt.Errorf("parsing %s: %w", paths.ConfigFile, err)
		}
		cfg.Merge(Default())
	} else if !os.IsNotExist(err) {
		return nil, paths, err
	}

	if cfg.CacheDir == "" {
		cfg.CacheDir = paths.CacheDir
	}
	return cfg, paths, nil
}

// Save writes the config file with 0600 permissions (it may hold API keys).
func Save(paths Paths, cfg *Config) error {
	if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(paths.ConfigFile, data, 0o600)
}

// DefaultMinecraftDir auto-detects the user's Minecraft installation.
//
// It probes, in order: an explicit config value, $MINECRAFT_DIR,
// launcher-specific defaults per platform, then the presence of a
// versions/ directory in the home directory.
func DefaultMinecraftDir() string {
	if v := os.Getenv("MODHARBOR_MINECRAFT_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".minecraft"),
		filepath.Join(home, ".local", "share", "minecraft"),
		filepath.Join(home, "Library", "Application Support", "minecraft"),
		filepath.Join(home, "curseforge", "minecraft", "Install"),
		filepath.Join(home, "Documents", "Curseforge", "minecraft", "Install"),
		filepath.Join(home, ".multimc", "instances"),
		filepath.Join(home, "Documents", "MultiMC", "instances"),
		filepath.Join(home, "Games", "itch", "minecraft"),
	}
	for _, c := range candidates {
		if isMinecraftDir(c) {
			return c
		}
	}
	return filepath.Join(home, ".minecraft")
}

func isMinecraftDir(p string) bool {
	fi, err := os.Stat(filepath.Join(p, "versions"))
	return err == nil && fi.IsDir()
}

// MinecraftDir returns the resolved Minecraft directory for cfg.
func (c *Config) MinecraftDirectory() string {
	if c.MinecraftDir != "" {
		return c.MinecraftDir
	}
	return DefaultMinecraftDir()
}

// NormalizeChannel canonicalises a version channel string.
func NormalizeChannel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "alpha", "a", "experimental":
		return "alpha"
	case "beta", "b", "snapshot", "pre":
		return "beta"
	case "release", "r", "stable", "":
		return "release"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}
