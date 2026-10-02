// Package app wires the pieces together: config, store, providers, resolver
// and the engine commands. It is the single place commands talk to.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/MohammadMD1383/modharbor/internal/config"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/provider/curseforge"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// App holds the shared, lazily-initialised dependencies for a CLI run.
type App struct {
	Config     *config.Config
	Paths      config.Paths
	Store      *store.Store
	Modrinth   *modrinth.Client
	CurseForge *curseforge.Client

	onceStore sync.Once
	onceMR    sync.Once
	onceCF    sync.Once

	// CacheTTL controls in-memory response caching.
	CacheTTL time.Duration
}

// New builds an App from a config and its paths.
func New(cfg *config.Config, paths config.Paths) *App {
	return &App{Config: cfg, Paths: paths, CacheTTL: 10 * time.Minute}
}

// State returns the persistent store, opening it on first use.
func (a *App) State() (*store.Store, error) {
	var err error
	a.onceStore.Do(func() {
		a.Store, err = store.Open(a.Paths.StateFile)
	})
	if a.Store == nil && err == nil {
		return nil, fmt.Errorf("state store unavailable")
	}
	return a.Store, err
}

// MR returns the Modrinth client.
func (a *App) MR() *modrinth.Client {
	a.onceMR.Do(func() {
		a.Modrinth = modrinth.New(modrinth.Options{
			BaseURL:   a.Config.Modrinth.BaseURL,
			CacheTTL:  a.CacheTTL,
			UserAgent: modrinth.UserAgent,
		})
	})
	return a.Modrinth
}

// CF returns the CurseForge client, which is inert without an API key.
func (a *App) CF() *curseforge.Client {
	a.onceCF.Do(func() {
		a.CurseForge = curseforge.New(curseforge.Options{
			BaseURL:   a.Config.CurseForge.BaseURL,
			APIKey:    a.Config.CurseForge.APIKey,
			UserAgent: curseforge.UserAgent,
		})
	})
	return a.CurseForge
}

// HTTPClient returns a shared HTTP client for ad-hoc requests.
func (a *App) HTTPClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second}
}

// MinecraftDir returns the effective Minecraft directory.
func (a *App) MinecraftDir() string { return a.Config.MinecraftDirectory() }

// ResolveInstance turns a user reference into an instance.
func (a *App) ResolveInstance(ref string) (*instance.Info, error) {
	root := a.MinecraftDir()
	if ref == "" {
		ref = a.Config.DefaultInstance
	}
	if ref == "" {
		return nil, fmt.Errorf("no instance given and no defaultInstance configured")
	}
	inst, err := instance.Resolve(root, ref)
	if err != nil {
		return nil, err
	}
	a.recordInstance(inst)
	return inst, nil
}

// recordInstance stores an observation for later commands.
func (a *App) recordInstance(inst *instance.Info) {
	st, err := a.State()
	if err != nil || st == nil {
		return
	}
	st.PutInstance(store.InstanceRecord{
		InstanceID: inst.ID,
		Path:       inst.Path,
		MCVersion:  inst.MCVersion,
		Loader:     string(inst.Type),
		ModCount:   inst.ModCount,
		LastScan:   time.Now(),
	})
	_ = st.Save()
}

// Instances lists every discovered instance under the Minecraft directory.
func (a *App) Instances() ([]*instance.Info, error) {
	return instance.Discover(a.MinecraftDir())
}

// EnsureStateDir creates the data directory.
func (a *App) EnsureStateDir() error {
	return os.MkdirAll(a.Paths.DataDir, 0o755)
}

// Context returns the context for the current command, honouring cancellation
// from the caller.
func Context() context.Context { return context.Background() }

// Channel resolves the configured update channel.
func (a *App) Channel() modrinth.Channel {
	return modrinth.Channel(config.NormalizeChannel(a.Config.Update.Channel))
}

// Concurrency returns the configured parallelism.
func (a *App) Concurrency() int {
	n := a.Config.Update.Concurrency
	if n <= 0 {
		return 4
	}
	if n > 16 {
		return 16
	}
	return n
}
