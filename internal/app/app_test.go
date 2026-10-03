package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/config"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
)

// App is the wiring layer every command shares, so the pure policy helpers
// are pinned here: constructor defaults, concurrency clamping, channel
// canonicalisation, and the lazily-built clients returning one instance.
func TestNewSetsDefaultCacheTTL(t *testing.T) {
	a := New(config.Default(), config.Paths{})
	if a.CacheTTL != 10*time.Minute {
		t.Errorf("CacheTTL = %v, want 10m", a.CacheTTL)
	}
}

func TestConcurrencyClampsToBounds(t *testing.T) {
	for in, want := range map[int]int{
		0:  4,
		-3: 4,
		6:  6,
		16: 16,
		99: 16,
	} {
		cfg := config.Default()
		cfg.Update.Concurrency = in
		if got := New(cfg, config.Paths{}).Concurrency(); got != want {
			t.Errorf("Concurrency(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestChannelCanonicalisesConfigValue(t *testing.T) {
	for in, want := range map[string]modrinth.Channel{
		"":             modrinth.ChannelRelease,
		"stable":       modrinth.ChannelRelease,
		"beta":         modrinth.ChannelBeta,
		"snapshot":     modrinth.ChannelBeta,
		"experimental": modrinth.ChannelAlpha,
	} {
		cfg := config.Default()
		cfg.Update.Channel = in
		if got := New(cfg, config.Paths{}).Channel(); got != want {
			t.Errorf("Channel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContextAndHTTPClientAreUsable(t *testing.T) {
	if Context() == nil {
		t.Error("Context() = nil, want a background context")
	}
	c := New(config.Default(), config.Paths{}).HTTPClient()
	if c == nil {
		t.Fatal("HTTPClient() = nil, want a client")
	}
	if c.Timeout != 45*time.Second {
		t.Errorf("HTTPClient timeout = %v, want 45s", c.Timeout)
	}
}

func TestMinecraftDirPrefersExplicitConfig(t *testing.T) {
	cfg := config.Default()
	cfg.MinecraftDir = "/tmp/mh-explicit"
	if got := New(cfg, config.Paths{}).MinecraftDir(); got != "/tmp/mh-explicit" {
		t.Errorf("MinecraftDir() = %q, want explicit config value", got)
	}
}

func TestMinecraftDirFallsBackToEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MODHARBOR_MINECRAFT_DIR", dir)
	cfg := config.Default()
	cfg.MinecraftDir = ""
	if got := New(cfg, config.Paths{}).MinecraftDir(); got != dir {
		t.Errorf("MinecraftDir() = %q, want env dir %q", got, dir)
	}
}

func TestMRAndCFClientsAreMemoized(t *testing.T) {
	a := New(config.Default(), config.Paths{})
	if a.MR() == nil || a.MR() != a.Modrinth {
		t.Error("MR() does not memoize the Modrinth client")
	}
	if a.CF() == nil || a.CF() != a.CurseForge {
		t.Error("CF() does not memoize the CurseForge client")
	}
}

func TestEnsureStateDirCreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	a := New(config.Default(), config.Paths{DataDir: dir})
	if err := a.EnsureStateDir(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("EnsureStateDir() left no directory at %q (err=%v)", dir, err)
	}
}

func TestResolveInstanceWithoutRefOrDefaultErrors(t *testing.T) {
	t.Setenv("MODHARBOR_MINECRAFT_DIR", t.TempDir())
	a := New(config.Default(), config.Paths{})
	a.Config.DefaultInstance = ""
	if _, err := a.ResolveInstance(""); err == nil {
		t.Error("ResolveInstance(\"\") = nil error, want missing-instance error")
	}
}

func TestStateMemoizesStoreOnTempFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	a := New(config.Default(), config.Paths{StateFile: stateFile})
	first, err := a.State()
	if err != nil || first == nil {
		t.Fatalf("State() = (%v, %v), want a store", first, err)
	}
	second, err := a.State()
	if err != nil || second != first {
		t.Errorf("State() second call = (%v, %v), want the same store", second, err)
	}
}
