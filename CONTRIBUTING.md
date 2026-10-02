# Contributing to modharbor

Thanks for taking a look. modharbor is a small CLI with one dependency, so most
changes are local and reviewable.

## Development setup

Go 1.23 or newer is required.

```sh
git clone https://github.com/MohammadMD1383/modharbor
cd modharbor
go build ./...
```

Build a binary you can hack on:

```sh
go build -o ./modharbor ./cmd/modharbor
./modharbor --minecraft ~/.minecraft instances
```

Useful overrides while developing: `--config /tmp/config.json` to keep your real
config untouched, and `--minecraft <path>` to point at a scratch instance tree.
`--json` gives you stable output to assert against.

### Before you open a pull request

All four must be clean:

```sh
go build ./...      # compiles
go vet ./...        # no suspicious constructs
go test ./...       # all tests pass
gofmt -l internal/  # must print nothing
```

`gofmt -l` printing a filename means the file is not formatted; run
`gofmt -w internal/`.

### How the tests are written

There are two styles, and the split is deliberate:

- **Table-driven tests** for pure functions and pure decisions — name
  similarity, slug derivation, channel filtering, version comparison, metadata
  parsing. Each case is a named struct and the sub-test name says what it
  asserts:

  ```go
  cases := []struct {
      name string
      in   string
      want string
  }{
      {"yacl camel case", "YetAnotherConfigLib", "yet-another-config-lib"},
      {"ipn stays intact", "libIPN", "libipn"},
  }
  ```

- **Fixture-driven tests** for anything that touches the filesystem or the
  network. These use `t.TempDir()`, write synthetic jars with
  `writeZipFile`, and run against an `httptest.Server` that impersonates the
  Modrinth API (`fakeMR` in `internal/migrate/migrate_test.go`). No test in this
  repository reaches the real Modrinth API, and none should.

Tests that assert on files end up in `internal/migrate/migrate_test.go` —
including `TestDryRunLeavesFilesystemUntouched` and
`TestChecksumMismatchIsRejected`, which are the executable specification of the
safety guarantees in the README.

## Project layout

```
cmd/modharbor/main.go     entry point; nothing but os.Exit(cli.Execute())
internal/
  cli/                    the cobra command tree and all rendering
  app/                    dependency wiring: config + store + providers
  config/                 load/save config.json, XDG paths, defaults
  store/                  state.json: resolutions, response cache, instances
  instance/               instance discovery, version JSON, launcher sidecars
  modmeta/                read mod metadata out of jars (incl. nested jars)
  hashutil/               SHA-1/SHA-256/SHA-512 primitives
  resolver/               identification: the hash→CF→slug→name chain
  migrate/                the migration engine, downloads, backups, dedupe
  mrpack/                 Modrinth .mrpack read/write
  provider/modrinth/      Modrinth API v2 client
  provider/curseforge/    CurseForge API v1 client (optional, needs a key)
  ui/                     colour, symbols, tables, panels, prompts, progress
  version/                build metadata injected at link time
docs/                     architecture and provider notes
```

| Package | Responsibility | Does *not* |
| --- | --- | --- |
| `cli` | Parse flags, resolve the instance, call an engine, render through `ui`. Every command has the same shape: `bootstrap()` → resolve → delegate → render | Contain business logic, or touch the Modrinth API directly |
| `app` | Lazily construct the store and provider clients once per run; resolve instance references; record observations | Know anything about migration |
| `config` | The `Config` document, XDG path derivation, `0600` writes, channel normalisation | Read the network or the filesystem beyond its own files |
| `store` | One atomically-written JSON document: jar identities keyed by SHA-1, a TTL response cache, per-instance records | Decide anything about mods |
| `instance` | Find instances, detect loader from the version JSON libraries, read `TLauncherAdditional.json` / `mmc-pack.json` / `instance.cfg`, list jars | Know what a mod is |
| `modmeta` | Read `fabric.mod.json`, `quilt.mod.json`, `META-INF/mods.toml`, `mcmod.info`; normalise names; list jars nested under `jars/` and `META-INF/jars/` | Fetch anything |
| `hashutil` | One-pass multi-digest hashing, digest normalisation, verification | Know about Modrinth's hashing scheme |
| `resolver` | The identification chain and its corroboration rules | Write anything |
| `migrate` | Plan a migration, decide an action per mod, download atomically, back up replacements, collapse duplicates | Render output (returns a `Report`) |
| `mrpack` | Read and write `.mrpack` archives: identify by hash, export by URL, embed the rest as overrides, verify on import | Touch instances beyond the directory it is handed |
| `provider/*` | One API client each: request shaping, retries, typed payloads, in-memory response cache | Make policy decisions |
| `ui` | Everything the user sees: palette, glyphs, `Heading`, `Table`, `Panel`, `Task`, `Spinner`, `Prompter` | Know what a mod is |
| `version` | `Version`/`Commit`/`Date`, overridden by `-ldflags` in the release workflow | Contain logic |

The dependency direction is one-way:
`cli → app → {config, store, provider, instance}` and
`cli → migrate → {resolver, provider, instance, modmeta, store}`.
Nothing in `migrate`, `resolver` or `provider` imports `cli` or `ui`.

## Coding conventions

**Doc comments on exported identifiers.** Every exported type, function, method
and constant gets a comment starting with its name. Unexported helpers get one
when the *why* is not obvious from the code. Comment the reasoning, not the
mechanics:

```go
// backupReplacements moves files that will be replaced into a timestamped
// backup directory, so an interrupted migration is always recoverable.
func backupReplacements(modsDir string, results []Result) error {
```

**No new dependencies without discussion.** modharbor has exactly one
dependency, cobra, and it intends to keep it that way. The terminal UI, the
spinner, the tables, the prompts and the zip/TOML/JSON reading are all
hand-rolled on the standard library. If you think a new dependency is
unavoidable, open an issue first and say what it replaces.

**Keep commands shaped the same way.** Flags first, `bootstrap()`, resolve the
instance, delegate to an engine, render through `ui`. Use `fail()` for errors so
exit codes and styling stay consistent, and `exitWithCode()` for a non-zero exit
with nothing to say.

**Render through `ui`, never with `fmt.Println` of styled text.** Every command
must respect `--json`, `--no-color`, `NO_COLOR`, and non-TTY stdout. The one
exception is output that is *only* meaningful in a pipe: `cache path` and
`config get`/`config path`, which print a bare value.

**Safety invariants.** If you touch the migration engine, keep these true:

1. No file is renamed into `mods/` before its digest matches (sha512 for
   migration, sha1 for `.mrpack` imports — use whichever the format records).
2. A file is never removed before its replacement is on disk.
3. `--dry-run` performs no writes at all. `TestDryRunLeavesFilesystemUntouched`
   enforces this.
4. Nothing user-visible is dropped silently. Duplicates and unmatched mods are
   reported.
5. Backup and restore stay symmetric. `migrate/download.go` writes
   `.modharbor-backup/<stamp>/`; `cli/rollback.go` reads it. They each declare
   the layout as a local constant on purpose, so if you change one, change both
   and add a test that round-trips through `cli/rollback.go`.

**Interpret hashes and version strings defensively.** Mod packs are not a schema.
Fabric metadata is decoded with tolerant types (`flexAuthors`, `flexStringList`
in `modmeta`) because `"license": "MIT"` and `"authors": "me"` are both valid.
If a decode of some *other* field fails, fall back to reading only the fields
you need (`parseFabricMinimal`) rather than losing the mod's identity.

**Context:** long operations take a `context.Context` first argument and pass it
down. Filesystem and network calls honour it.

## Adding a command

1. Create `internal/cli/<name>.go` with a `new<Name>Cmd()` constructor
   (`newScanCmd`, `newUpdateCmd`, …).
2. Give it a `Use`, a one-line `Short`, a `Long` that says what it does *and*
   when you would reach for it, and an `Example`. `--help` output is the
   documentation, so make it good.
3. In `RunE`: `bootstrap()`, resolve the instance with `pickInstanceArg(args)`
   (which already falls back to `--instance` and the configured default),
   delegate, then render. Return `fail(...)` for errors.
4. Handle `flagJSON` first and return `printJSON(...)` — JSON mode must be
   non-interactive and must not prompt.
5. Register it in `newRootCmd()` in `internal/cli/root.go`.
6. Guard every prompt with `ui.IsInteractive()` so the command still works in
   a pipe or in CI.
7. Add the command to the table in `README.md` and a line to `CHANGELOG.md` under
   `## [Unreleased]`.
8. If the command does something interesting, add a test — most will want a
   `fakeMR` and a `t.TempDir()`.

## Adding a provider

A provider is a self-contained client package plus a hook in the resolver.
There is no interface to satisfy today, deliberately: with two providers, an
abstraction would be speculative. Add the third when the third exists, and shape
it then.

**1. Write the client** in `internal/provider/<name>/`, following
`internal/provider/modrinth/modrinth.go`:

- A `Client` struct holding `baseURL`, an `*http.Client`, a `User-Agent`, and an
  in-memory response cache guarded by a mutex.
- `New(Options) *Client`, with `DefaultBaseURL` and `UserAgent` constants. The
  User-Agent must be descriptive and must name the project URL; Modrinth blocks
  generic agents.
- One private `do(ctx, method, path, body, query, out)` that owns **all** error
  handling: `404 → ErrNotFound`, `429 → honour Retry-After and retry`,
  `5xx → retry with exponential backoff`, `4xx → typed `*APIError`, everything
  else decoded. Four attempts maximum. Never let a transport error escape
  unretried.
- Typed payloads with `json` tags matching the upstream wire format, plus small
  helper methods (`Version.PrimaryFile()`, `Version.Supports(mc, loader)`).
- Cache reads and writes for every GET. Resolution asks for the same project
  several times while identifying one mods folder.

See [docs/providers.md](docs/providers.md) for the full guide.

**2. If it is optional**, make the client refuse to make requests without
credentials (`ErrNoAPIKey`), and make `app.App` construct it lazily behind
`CF()`-style accessor with a `sync.Once`.

**3. Add the config stanza** to `config.Config` (a `ProviderConfig` with
`enabled`, `apiKey`, `baseUrl`), to `config.Default()`, and to
`configMerge`'s zero-value fill. `config.Save` already writes the file `0600`.

**4. Wire it into identification.** In `resolver.Resolve`, add a strategy
between the existing steps with its own `Method*` constant and `conf*` floor in
the confidence block at the top of the file. Every strategy returns a
`*store.Resolution` or `nil`, and `Resolve` takes the first non-nil. Keep
strategies ordered strongest-first and give yours a confidence that reflects
how much evidence it actually used.

**5. Mark CurseForge-only / provider-only projects honestly.** A project id
from one provider is not valid in another. Record the provider in
`store.Resolution.Provider` so the engine can report "published on X only"
instead of silently probing a foreign id space.

**6. Add the provider to `scan`'s `SOURCE` column** in
`internal/cli/scan.go`'s `methodLabel`, so the confidence chain stays visible to
users.

## Releasing

`internal/version.Version` is the source of truth. The release workflow injects
`Version`, `Commit` and `Date` with `-ldflags`, builds `cmd/modharbor` for
linux/darwin/windows on amd64/arm64, and attaches the binaries to the GitHub
release. Bump `Version` in `internal/version/version.go`, add a
`## [x.y.z] - YYYY-MM-DD` section to `CHANGELOG.md`, and tag.