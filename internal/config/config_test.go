package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolate points every path this package can reach at a temp directory and
// returns that stand-in home.
//
// HOME and USERPROFILE are both overridden because os.UserHomeDir reads the
// latter on Windows. MODHARBOR_MINECRAFT_DIR is cleared so the probes in
// DefaultMinecraftDir run against the fake home rather than against whatever
// the shell happens to export — the real instances must never be stat'ed by a
// test run.
func isolate(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MODHARBOR_MINECRAFT_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	return home
}

// writeConfig drops a raw config document on disk without going through Save,
// so Load can be exercised against hand-written JSON.
func writeConfig(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ─── defaults ────────────────────────────────────────────────────────────────

// The defaults are what every other package assumes before a config file is
// ever read, so they are pinned exactly rather than "close enough".
func TestDefaultEnablesBothProviders(t *testing.T) {
	cfg := Default()

	if !cfg.Modrinth.Enabled || !cfg.CurseForge.Enabled {
		t.Errorf("providers must be enabled by default, got modrinth=%v curseForge=%v",
			cfg.Modrinth.Enabled, cfg.CurseForge.Enabled)
	}
	for _, c := range []struct{ name, got, want string }{
		{"modrinth base url", cfg.Modrinth.BaseURL, "https://api.modrinth.com/v2"},
		{"curseforge base url", cfg.CurseForge.BaseURL, "https://api.curseforge.com/v1"},
		{"channel", cfg.Update.Channel, "release"},
	} {
		if c.got != c.want {
			t.Errorf("default %s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if cfg.Update.Concurrency != 6 {
		t.Errorf("default concurrency = %d, want 6", cfg.Update.Concurrency)
	}
	// Both safety switches default on; a config that omits them must not turn
	// off download verification or the pre-change backup.
	if !cfg.Update.VerifyDownloads {
		t.Error("default verifyDownloads = false, want true")
	}
	if !cfg.Update.AutoBackup {
		t.Error("default autoBackup = false, want true")
	}
}

// ─── merge ───────────────────────────────────────────────────────────────────

func TestMergeFillsZeroValuesOnly(t *testing.T) {
	def := &Config{
		MinecraftDir:    "/default/mc",
		DefaultInstance: "default-instance",
		CacheDir:        "/default/cache",
		Update:          UpdateConfig{Channel: "release", Concurrency: 6},
		Modrinth:        ProviderConfig{BaseURL: "https://api.modrinth.com/v2"},
		CurseForge:      ProviderConfig{BaseURL: "https://api.curseforge.com/v1"},
	}
	c := &Config{
		MinecraftDir: "/chosen/mc",
		CacheDir:     "/chosen/cache",
		Update: UpdateConfig{
			Channel:         "beta",
			Concurrency:     3,
			VerifyDownloads: false,
			AutoBackup:      false,
		},
		Modrinth:   ProviderConfig{Enabled: false, BaseURL: "https://mirror.example/v2"},
		CurseForge: ProviderConfig{BaseURL: "https://mirror.example/v1"},
	}

	c.Merge(def)

	if c.MinecraftDir != "/chosen/mc" {
		t.Errorf("MinecraftDir = %q, want the configured value kept", c.MinecraftDir)
	}
	if c.CacheDir != "/chosen/cache" {
		t.Errorf("CacheDir = %q, want the configured value kept", c.CacheDir)
	}
	if c.DefaultInstance != "default-instance" {
		t.Errorf("DefaultInstance = %q, want the default filled in", c.DefaultInstance)
	}
	if c.Update.Channel != "beta" {
		t.Errorf("Channel = %q, want the configured value kept", c.Update.Channel)
	}
	if c.Update.Concurrency != 3 {
		t.Errorf("Concurrency = %d, want the configured value kept", c.Update.Concurrency)
	}
	if c.Modrinth.BaseURL != "https://mirror.example/v2" {
		t.Errorf("Modrinth.BaseURL = %q, want the configured mirror kept", c.Modrinth.BaseURL)
	}
	// A false bool is a decision, not a missing field: turning a provider off
	// must survive a merge.
	if c.Modrinth.Enabled {
		t.Error("Merge re-enabled a provider the config had disabled")
	}
	if c.Update.VerifyDownloads || c.Update.AutoBackup {
		t.Error("Merge flipped a false bool back to true")
	}
}

func TestMergeFillsBaseURLsAndNegativeConcurrency(t *testing.T) {
	def := Default()
	for _, in := range []int{0, -1} {
		c := &Config{Update: UpdateConfig{Concurrency: in}}
		c.Merge(def)
		if c.Update.Concurrency != def.Update.Concurrency {
			t.Errorf("Merge with concurrency %d = %d, want %d", in, c.Update.Concurrency, def.Update.Concurrency)
		}
	}

	c := &Config{}
	c.Merge(def)
	if c.Modrinth.BaseURL != def.Modrinth.BaseURL {
		t.Errorf("Modrinth.BaseURL = %q, want %q", c.Modrinth.BaseURL, def.Modrinth.BaseURL)
	}
	if c.CurseForge.BaseURL != def.CurseForge.BaseURL {
		t.Errorf("CurseForge.BaseURL = %q, want %q", c.CurseForge.BaseURL, def.CurseForge.BaseURL)
	}
	if c.Update.Channel != def.Update.Channel {
		t.Errorf("Channel = %q, want %q", c.Update.Channel, def.Update.Channel)
	}
}

// ─── paths ───────────────────────────────────────────────────────────────────

func TestDefaultPathsDerivesFromXDG(t *testing.T) {
	home := isolate(t)

	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}

	// The two joins hold on every platform: every leaf is named config.json /
	// state.json inside its directory.
	if p.ConfigFile != filepath.Join(p.ConfigDir, "config.json") {
		t.Errorf("ConfigFile = %q, want it inside ConfigDir %q", p.ConfigFile, p.ConfigDir)
	}
	if p.StateFile != filepath.Join(p.DataDir, "state.json") {
		t.Errorf("StateFile = %q, want it inside DataDir %q", p.StateFile, p.DataDir)
	}
	// XDG_DATA_HOME is read here rather than via os.UserConfigDir, so it
	// applies on macOS too, where os.UserConfigDir ignores it.
	if want := filepath.Join(home, "data", "modharbor"); p.DataDir != want {
		t.Errorf("DataDir = %q, want %q", p.DataDir, want)
	}
	// os.UserConfigDir/UserCacheDir only honour XDG on Linux; elsewhere they
	// return the platform location, which is still the answer.
	if runtime.GOOS == "linux" {
		if want := filepath.Join(home, "config", "modharbor"); p.ConfigDir != want {
			t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, want)
		}
		if want := filepath.Join(home, "cache", "modharbor"); p.CacheDir != want {
			t.Errorf("CacheDir = %q, want %q", p.CacheDir, want)
		}
	}
	if filepath.Base(p.ConfigDir) != "modharbor" || filepath.Base(p.CacheDir) != "modharbor" {
		t.Errorf("config/cache dirs must live under a modharbor directory, got %q and %q", p.ConfigDir, p.CacheDir)
	}
}

func TestDefaultPathsFallsBackToDotLocalShare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses AppData, not XDG_DATA_HOME")
	}
	home := isolate(t)
	t.Setenv("XDG_DATA_HOME", "")

	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "share", "modharbor"); p.DataDir != want {
		t.Errorf("DataDir = %q, want %q", p.DataDir, want)
	}
}

func TestDefaultPathsUsesAppDataOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("XDG_DATA_HOME is the answer everywhere else")
	}
	home := isolate(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))

	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	// On Windows the XDG variable is deliberately ignored: the launcher and
	// the other TLauncher-era tools keep state under %LOCALAPPDATA%.
	if want := filepath.Join(home, "AppData", "Local", "modharbor"); p.DataDir != want {
		t.Errorf("DataDir = %q, want %q", p.DataDir, want)
	}
}

// ─── load ────────────────────────────────────────────────────────────────────

func TestLoadMissingFileYieldsDefaultsWithoutWriting(t *testing.T) {
	isolate(t)

	cfg, paths, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.Channel != "release" || cfg.Update.Concurrency != 6 {
		t.Errorf("missing config did not yield defaults: channel=%q concurrency=%d",
			cfg.Update.Channel, cfg.Update.Concurrency)
	}
	if cfg.Modrinth.BaseURL != "https://api.modrinth.com/v2" {
		t.Errorf("Modrinth.BaseURL = %q, want the default API root", cfg.Modrinth.BaseURL)
	}
	// CacheDir has no in-document default, so Load points it at the XDG cache.
	if cfg.CacheDir != paths.CacheDir {
		t.Errorf("CacheDir = %q, want the derived cache dir %q", cfg.CacheDir, paths.CacheDir)
	}
	// A read must not create the file: `modharbor version` should not litter
	// whatever path --config happened to name.
	if _, err := os.Stat(paths.ConfigFile); err == nil {
		t.Errorf("Load created %q, want no side effect", paths.ConfigFile)
	}
}

func TestLoadHonoursExplicitPath(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "nested", "custom.json")
	writeConfig(t, path, `{
	  "minecraftDir": "/srv/mc",
	  "defaultInstance": "pack",
	  "curseForge": { "enabled": false, "apiKey": "literal-test-key" }
	}`)

	cfg, paths, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigFile != path {
		t.Errorf("ConfigFile = %q, want the --config path %q", paths.ConfigFile, path)
	}
	if cfg.MinecraftDir != "/srv/mc" {
		t.Errorf("MinecraftDir = %q, want /srv/mc", cfg.MinecraftDir)
	}
	if cfg.DefaultInstance != "pack" {
		t.Errorf("DefaultInstance = %q, want pack", cfg.DefaultInstance)
	}
	if cfg.CurseForge.APIKey != "literal-test-key" {
		t.Errorf("CurseForge.APIKey = %q, want the stored key back", cfg.CurseForge.APIKey)
	}
	// An explicit false must not be re-defaulted to true by the merge.
	if cfg.CurseForge.Enabled {
		t.Error("CurseForge.Enabled = true, want the configured false kept")
	}
	// Fields the document omits keep their defaults.
	if cfg.Update.Channel != "release" || !cfg.Update.VerifyDownloads {
		t.Errorf("omitted fields lost their defaults: channel=%q verify=%v", cfg.Update.Channel, cfg.Update.VerifyDownloads)
	}
	if !cfg.Modrinth.Enabled {
		t.Error("Modrinth.Enabled = false, want the default true")
	}
}

func TestLoadKeepsExplicitFalseAndConcurrency(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, `{"update": {"verifyDownloads": false, "autoBackup": false, "concurrency": 3}}`)

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.VerifyDownloads || cfg.Update.AutoBackup {
		t.Error("explicit false flags were reset to true")
	}
	if cfg.Update.Concurrency != 3 {
		t.Errorf("Concurrency = %d, want the configured 3", cfg.Update.Concurrency)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, `{"minecraftDir": }`)

	cfg, paths, err := Load(path)
	if err == nil {
		t.Fatal("Load of malformed JSON returned no error")
	}
	if cfg != nil {
		t.Errorf("Load returned a config alongside the error: %+v", cfg)
	}
	// The message must name the file, otherwise the user cannot tell which of
	// several configs is broken.
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %q", err, path)
	}
	if paths.ConfigFile != path {
		t.Errorf("ConfigFile = %q, want the attempted path %q", paths.ConfigFile, path)
	}
}

func TestLoadSurfacesUnreadableConfig(t *testing.T) {
	isolate(t)
	// A directory where the file should be: ReadFile fails, and the failure is
	// not "does not exist", so it must be returned rather than swallowed into
	// a silent fall back to defaults.
	dir := filepath.Join(t.TempDir(), "config.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(dir)
	if err == nil {
		t.Fatal("Load of an unreadable config returned no error")
	}
	if cfg != nil {
		t.Errorf("Load returned a config alongside the error: %+v", cfg)
	}
	if strings.Contains(err.Error(), "parsing") {
		t.Errorf("error %q is reported as a parse failure, want the read failure", err)
	}
}

// ─── save ────────────────────────────────────────────────────────────────────

func TestSaveWritesPrivateFileAndRoundTrips(t *testing.T) {
	isolate(t)
	dir := filepath.Join(t.TempDir(), "cfg", "modharbor")
	paths := Paths{
		ConfigDir:  dir,
		ConfigFile: filepath.Join(dir, "config.json"),
	}

	cfg := Default()
	cfg.MinecraftDir = "/srv/mc"
	cfg.Modrinth.APIKey = "literal-test-key"
	cfg.Update.Concurrency = 11
	cfg.Display.NoColour = true
	if err := Save(paths, cfg); err != nil {
		t.Fatal(err)
	}

	// The directory holds an API key, so the file must not be group- or
	// world-readable.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(paths.ConfigFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("config mode = %v, want 0600", got)
		}
	}

	data, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	// The trailing newline keeps the file diff-friendly in a config the user
	// hand-edits.
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("config file must end with a newline, got %d bytes ending %q", len(data), tail(data))
	}

	back, _, err := Load(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if back.MinecraftDir != "/srv/mc" || back.Modrinth.APIKey != "literal-test-key" {
		t.Errorf("round trip lost values: %+v", back)
	}
	if back.Update.Concurrency != 11 || !back.Display.NoColour {
		t.Errorf("round trip lost update/display values: %+v %+v", back.Update, back.Display)
	}
	// Writing a config and reading it back must not mutate the document: an
	// omitted optional field stays absent rather than gaining a zero value.
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["minecraftDir"]; !ok {
		t.Error("minecraftDir missing from the written document")
	}
	if _, ok := raw["defaultInstance"]; ok {
		t.Error("an unset defaultInstance must be omitted (omitempty), not written as \"\"")
	}
}

func TestSaveCreatesConfigDir(t *testing.T) {
	isolate(t)
	dir := filepath.Join(t.TempDir(), "a", "b", "modharbor")
	paths := Paths{ConfigDir: dir, ConfigFile: filepath.Join(dir, "config.json")}

	if err := Save(paths, Default()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(paths.ConfigFile)
	if err != nil {
		t.Fatalf("Save did not create the config file: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("Save wrote an empty config file")
	}
}

func TestSaveReportsUnusableConfigDir(t *testing.T) {
	isolate(t)
	// A plain file where the config directory should be: the write cannot
	// proceed, and the failure must surface rather than being swallowed into a
	// "saved" that never happened.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	paths := Paths{ConfigDir: filepath.Join(file, "modharbor"), ConfigFile: filepath.Join(file, "config.json")}

	if err := Save(paths, Default()); err == nil {
		t.Error("Save into an unusable config dir returned no error")
	}
}

func TestSaveTightensAnExistingWorldReadableConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits; Windows ACLs are out of scope for this check")
	}
	isolate(t)
	dir := filepath.Join(t.TempDir(), "modharbor")
	paths := Paths{ConfigDir: dir, ConfigFile: filepath.Join(dir, "config.json")}

	// A config that already exists — hand-created, restored from a backup, or
	// written by an older build — may be group- or world-readable.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.Modrinth.APIKey = "literal-test-key"
	if err := Save(paths, cfg); err != nil {
		t.Fatal(err)
	}

	// os.WriteFile only applies its perm when it creates the file, so writing
	// over an existing 0644 config would otherwise leave the API key readable
	// by every local user. SECURITY.md promises 0600, so Save must enforce it
	// rather than assume it.
	fi, err := os.Stat(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("Save left a pre-existing config at mode %v, want it tightened to 0600", got)
	}
}

func TestSaveCreatesTheParentOfAnExplicitConfigPath(t *testing.T) {
	isolate(t)
	// `--config /somewhere/else/my.json` sets ConfigFile but leaves ConfigDir
	// at its XDG default, so the directory Save must create is the parent of
	// ConfigFile, not ConfigDir.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere", "nested")
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	paths.ConfigFile = filepath.Join(elsewhere, "my.json")
	if paths.ConfigDir == filepath.Dir(paths.ConfigFile) {
		t.Fatalf("test needs ConfigDir %q to differ from the config file's parent", paths.ConfigDir)
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Fatalf("test needs %q not to exist yet", elsewhere)
	}

	if err := Save(paths, Default()); err != nil {
		t.Fatalf("Save to an explicit path outside the XDG config dir failed: %v", err)
	}
	if _, err := os.Stat(paths.ConfigFile); err != nil {
		t.Errorf("Save did not create %q: %v", paths.ConfigFile, err)
	}
}

// ─── minecraft directory ─────────────────────────────────────────────────────

func TestDefaultMinecraftDirPrefersEnvVar(t *testing.T) {
	home := isolate(t)
	// A real installation exists too; the explicit override still wins.
	mkVersions(t, filepath.Join(home, ".minecraft"))
	dir := t.TempDir()
	t.Setenv("MODHARBOR_MINECRAFT_DIR", dir)

	if got := DefaultMinecraftDir(); got != dir {
		t.Errorf("DefaultMinecraftDir() = %q, want the MODHARBOR_MINECRAFT_DIR %q", got, dir)
	}
}

func TestDefaultMinecraftDirProbesCandidatesInOrder(t *testing.T) {
	home := isolate(t)
	mkVersions(t, filepath.Join(home, ".minecraft"))
	mkVersions(t, filepath.Join(home, ".local", "share", "minecraft"))
	mkVersions(t, filepath.Join(home, "curseforge", "minecraft", "Install"))

	if got, want := DefaultMinecraftDir(), filepath.Join(home, ".minecraft"); got != want {
		t.Errorf("DefaultMinecraftDir() = %q, want the first candidate %q", got, want)
	}

	// The order matters beyond the first entry: with only two launcher
	// installations present the earlier one must win, because picking up the
	// wrong instance means migrating somebody's game data by accident.
	home = isolate(t)
	mkVersions(t, filepath.Join(home, ".multimc", "instances"))
	mkVersions(t, filepath.Join(home, "Documents", "MultiMC", "instances"))
	if got, want := DefaultMinecraftDir(), filepath.Join(home, ".multimc", "instances"); got != want {
		t.Errorf("DefaultMinecraftDir() = %q, want the earlier candidate %q", got, want)
	}
}

func TestDefaultMinecraftDirSkipsCandidatesWithoutVersions(t *testing.T) {
	home := isolate(t)
	// A .minecraft directory with no versions/ is not an installation, and a
	// plain file called versions must not pass for one either.
	if err := os.MkdirAll(filepath.Join(home, ".minecraft", "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".minecraft", "versions"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mkVersions(t, filepath.Join(home, ".multimc", "instances"))

	if got, want := DefaultMinecraftDir(), filepath.Join(home, ".multimc", "instances"); got != want {
		t.Errorf("DefaultMinecraftDir() = %q, want the first real candidate %q", got, want)
	}
}

func TestDefaultMinecraftDirFallsBackToDotMinecraft(t *testing.T) {
	home := isolate(t)

	if got, want := DefaultMinecraftDir(), filepath.Join(home, ".minecraft"); got != want {
		t.Errorf("DefaultMinecraftDir() = %q, want the fallback %q", got, want)
	}
}

func TestDefaultMinecraftDirReturnsEmptyWithoutAHome(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	// With no home directory there is nothing to probe and no safe guess. An
	// empty string makes the caller report the problem; inventing a relative
	// path here would put the instance in the current working directory.
	if got := DefaultMinecraftDir(); got != "" {
		t.Errorf("DefaultMinecraftDir() = %q, want \"\" when there is no home directory", got)
	}
}

func TestMinecraftDirectoryPrefersConfigOverProbe(t *testing.T) {
	home := isolate(t)
	mkVersions(t, filepath.Join(home, ".minecraft"))

	c := &Config{MinecraftDir: filepath.Join(home, "elsewhere")}
	if got := c.MinecraftDirectory(); got != c.MinecraftDir {
		t.Errorf("MinecraftDirectory() = %q, want the configured %q", got, c.MinecraftDir)
	}

	detected := filepath.Join(home, ".minecraft")
	c = &Config{}
	if got := c.MinecraftDirectory(); got != detected {
		t.Errorf("MinecraftDirectory() = %q, want the detected %q", got, detected)
	}
}

// ─── channels ────────────────────────────────────────────────────────────────

func TestNormalizeChannel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty means release", "", "release"},
		{"already canonical", "release", "release"},
		{"mixed case", "Beta", "beta"},
		{"surrounding space", "  beta  ", "beta"},
		{"single letter r", "r", "release"},
		{"stable synonym", "stable", "release"},
		{"snapshot maps to beta", "snapshot", "beta"},
		{"pre maps to beta", "pre", "beta"},
		{"experimental maps to alpha", "experimental", "alpha"},
		{"single letter a", "a", "alpha"},
		// An unrecognised channel is passed through lowercased rather than
		// silently becoming release: picking a version nobody asked for is
		// worse than a bad filter.
		{"unknown passes through", "Nightly", "nightly"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeChannel(c.in); got != c.want {
				t.Errorf("NormalizeChannel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// mkVersions creates the versions/ directory that marks a Minecraft install.
func mkVersions(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(dir, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// tail is the last few bytes of b, for failure messages.
func tail(b []byte) string {
	if len(b) > 16 {
		b = b[len(b)-16:]
	}
	return string(b)
}
