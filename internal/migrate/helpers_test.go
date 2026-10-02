package migrate

import (
	"archive/zip"
	"io"
	"os"

	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/resolver"
	"github.com/MohammadMD1383/modharbor/internal/store"
)

// TB is the subset of testing.TB these helpers need.
type TB interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// writeZipFile builds a jar containing the given entries.
func writeZipFile(t TB, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// newTestResolver builds a resolver backed by a fake Modrinth API.
func newTestResolver(cli *modrinth.Client, st *store.Store) *resolver.Resolver {
	return resolver.New(resolver.Options{Modrinth: cli, Cache: st})
}
