# Architecture

How modharbor is put together, and why.

The short version: a command parses flags and resolves an instance, hands the
work to an engine, and the engine asks a resolver what each jar *is* before it
can decide what the target instance *should* contain. Everything below follows
from that ordering — you cannot pick a version until you know the project.

## Package map

```
                    cmd/modharbor
                          │
                    internal/cli          cobra tree, flags, rendering
                          │
              ┌───────────┴────────────┐
              │                        │
        internal/app            internal/migrate      the engine
              │                        │
              │            ┌───────────┼───────────┐
              │            │           │           │
              │        resolver    instance    modmeta      hashutil
              │            │
              │        provider/modrinth   provider/curseforge
              │
          config    store

  everything renders through internal/ui
```

| Package | Key files | Role |
| --- | --- | --- |
| `cmd/modharbor` | `main.go` | `os.Exit(cli.Execute())`. Nothing else. |
| `cli` | `root.go`, `scan.go`, `migrate.go`, `update.go`, `outdated.go`, `list.go`, `add.go`, `doctor.go`, `deps.go`, `link.go`, `rollback.go`, `mrpack.go`, `search.go`, `cache.go`, `install.go`, `download.go`, `helpers.go`, `version.go` | Command tree, flag definitions, and every byte the user sees |
| `app` | `app.go` | `App` — the only place the store and provider clients are constructed. Lazily, once each, via `sync.Once` |
| `config` | `config.go` | The `Config` document, `Paths`, XDG resolution, `Save` at `0600`, `NormalizeChannel`, Minecraft-directory auto-detection |
| `store` | `store.go` | `state.json`: `Resolutions` keyed by SHA-1, a TTL `Cache`, and per-instance records. `sync.RWMutex` + atomic temp-file-and-rename writes |
| `instance` | `instance.go` | `Discover`, `Load`, `Resolve`, `ModFiles`, `ParseTLauncherMods`, launcher sidecar parsing, Minecraft-version extraction |
| `modmeta` | `modmeta.go`, `nested.go` | `Read` (four descriptor formats), `NormalizeName`, `GuessModIDFromFileName`, `ReadNested`, `ProvidedIDs` |
| `hashutil` | `hash.go` | `Multi` (one-pass SHA-1/256/512), `SHA1File`, `Normalize`, `Verify` |
| `resolver` | `resolve.go` | The identification chain and its corroboration rules |
| `migrate` | `migrate.go`, `download.go`, `dedupe.go` | `Engine.Run`: plan, decide, download, back up, dedupe |
| `mrpack` | `mrpack.go`, `export.go`, `install.go`, `required.go` | `.mrpack` archives: `Load`/`Save`, `Export` (identify by hash, reference by URL, override the rest), `Install` (verify, skip what is present, extract overrides), `RequiredMods` |
| `provider/modrinth` | `modrinth.go` | API v2 client, `Channel`, `FilterByChannel`, `TypeRank`, `CDNURL` |
| `provider/curseforge` | `curseforge.go` | Optional API v1 client, fingerprint hydration, `ErrNoAPIKey` |
| `ui` | `ui.go`, `color.go`, `table.go`, `progress.go`, `prompt.go`, `human.go`, `width.go`, `secret_*.go`, `term_*.go` | Palette, glyphs, `Heading`, `Table`, `Panel`, `Task`, `Spinner`, `Progress`, `Prompter`, width/colour detection |
| `version` | `version.go` | `Version`/`Commit`/`Date`, injected by `-ldflags` |

Dependency direction is strictly one-way. Nothing in `migrate`, `resolver`,
`mrpack` or `provider/*` imports `cli` or `ui`; the engine returns a `Report`
and the CLI decides how to draw it. `internal/cli` stays thin enough that most
of its logic is worth testing through the engine below it.

`mrpack` is the one exception worth noting: `Export` constructs its own
`modrinth.Client` from `config.Load("")` rather than taking one as a parameter,
so a mirror or test server configured by the user is honoured without threading
a client through the CLI.

## Data flow: command → engine → resolver → provider

The whole pipeline, traced through `modharbor migrate a b`:

**1. Command.** `cli.newMigrateCmd()` builds the `*cobra.Command`.
`RunE` calls `bootstrap()` (`cli/root.go`), which loads config, applies the
global `--minecraft` and `--channel` overrides, merges defaults, and returns an
`*app.App`.

**2. Instance resolution.** `a.ResolveInstance(ref)` in `app/app.go` falls back
to `Config.DefaultInstance` when `ref` is empty, then calls
`instance.Resolve(root, ref)`. That tries, in order: a direct directory path, an
exact version-id match, a prefix match, then a substring match — so `26.3`,
`fabric` and `26.3-fabric-mod` all land on the right instance. `instance.Load`
parses `<id>.json`, detects the loader from the library list
(`instance.Load`'s switch over `net.fabricmc` / `org.quiltmc` /
`neoforged` / `net.minecraftforge`) with `mainClass` as a fallback, then
resolves the Minecraft version from arguments → launcher sidecars → the version
id. The resolved instance is recorded in the store.

**3. Engine construction.** The command builds the two collaborators:

```go
eng := migrate.New(a.MR(), resolver.New(resolver.Options{
    Modrinth: a.MR(), Cache: st,
}), st)
```

`a.MR()` returns the process-wide `*modrinth.Client`, so the in-memory response
cache is shared by every resolution in the run.

**4. Engine.Run.** `migrate.Engine.Run(ctx, src, dst, opts)` in
`internal/migrate/migrate.go`:

```go
srcFiles  := instance.ModFiles(src.ModsDirOrDefault())
dstFiles  := instance.ModFiles(dst.ModsDirOrDefault())
dstIndex  := e.indexTarget(ctx, dst, dstFiles)   // project-id -> targetEntry
tlMods    := instance.ParseTLauncherMods(src.Path)
cands     := e.candidatesFor(ctx, src, srcFiles, tlMods)
cands     = applyFilters(cands, opts)            // --include / --exclude
scoped    := e.res.ForInstance(dst.MCVersion, loaderName(dst.Type))
results   := scoped.ResolveAll(ctx, cands)
```

`indexTarget` is what makes `keep` decisions cheap: it maps every jar already in
the *target* to an upstream project id (from the cache where possible, else by
resolving now), so comparing against what is already installed costs no extra
downloads.

`candidatesFor` builds one `resolver.Candidate` per jar with its SHA-1, size,
parsed metadata, and — if the launcher recorded it — the CurseForge project id
and display name.

`ForInstance` returns a *scoped* resolver: a distinct struct sharing the
version memo and the mutex pointer, so corroboration lookups are reused across
the whole run but the resolver can be pinned to the target's game version and
loader. This is why `Resolver` is never copied after construction.

**5. Resolver.** `Resolver.ResolveAll` fans out with a 6-goroutine semaphore.
Each candidate goes through `Resolver.Resolve`, which consults the cache first
(a `manual` resolution wins outright; any cached project id is reused with a
refreshed source file name) and then the chain. See below.

**6. Provider.** Each strategy issues Modrinth calls through
`modrinth.Client`. Every GET goes through the single private `do` method, which
owns retries, `Retry-After`, `ErrNotFound` and the response cache. Version
listings used by corroboration are memoised per project id in
`Resolver.versions` behind `versionsMu`, because one mods folder asks about the
same project repeatedly.

**7. Decision.** Back in `Engine.Run`, each `resolver.Result` becomes a
`migrate.Result`:

- `MethodUnmatched` → `Engine.handleUnmatched` → `ActionUnmatched`, or
  `ActionCopy` when `--copy-unknown` is set.
- otherwise → `Engine.decide` → `Engine.bestVersion` asks the provider for the
  newest build matching the target's game version and loader, then compares it
  against `dstIndex` to pick `ActionInstall`, `ActionReplace` or `ActionKeep`.

`Engine.explainEmpty` and `Engine.excludeReason` are why a skip always has a
specific, actionable reason.

**8. Ordering and dedupe.** `dedupeByProject` collapses results that target the
same project (keeping the newest source version, marking the rest
`ActionDuplicate`), and results are sorted with unmatched mods last so the rows
needing attention are where the eye lands.

**9. Materialise.** Unless `opts.DryRun`, `Engine.materialize` backs up
replacements, then runs installs and copies concurrently, then replacements
sequentially, then writes the new identities back to the cache.

**10. Render.** The CLI turns the `*migrate.Report` into `ui.Task` rows grouped
by action, a boxed `Summary` panel, and — for a real run — an explicit list of
everything that still needs a human decision.

## The identification chain

`internal/resolver/resolve.go`. `Resolve` returns the first strategy that
produces a **corroborated** match; a strategy that produces a plausible but
uncorroborated match returns `nil` and the chain continues.

### 0. Cache

A `manual` resolution (`modharbor link`) is returned immediately and is never
overwritten. Any other cached resolution with a project id is reused, with
`SourceFile` refreshed to the current jar name — jar names change between
launcher versions, identities do not.

### 1. `hash` — confidence 1.00

`Resolver.byHash` calls `modrinth.Client.VersionByHash`, i.e.
`GET /v2/version_file/{sha1}`. The version payload carries no title, so
`mr.Project` is fetched for one; a failure there is not fatal.

If the resolver is scoped to an instance and the matched version does *not*
support that Minecraft version, confidence drops to 0.95
(`confHash - 0.05`). The match is deliberately kept: a mod that only has a 26.2
build is still the mod you need identified in order to report "no compatible
build".

### 2. `curseforge-id` — confidence 0.95

`Resolver.byCurseForgeID` runs only when the candidate carries a CurseForge
project id, which means the launcher wrote down what it installed. The
launcher's display name is searched on Modrinth via `findProjectByName`, which
tries exact slug, then exact normalised title, then containment. On a hit the
resolution is `Provider: "modrinth"`.

On a miss it does **not** probe Modrinth with the numeric id — CurseForge and
Modrinth id spaces are unrelated. It records `Provider: "curseforge"` with the
numeric id, and `migrate.Engine.decide` turns that into
`published on CurseForge only`.

### 3. `slug` — confidence 0.90

`Resolver.bySlug` derives candidate slugs from the mod id, the display name and
the launcher's name, in three forms each (verbatim lowercased, compacted,
hyphenated) via `candidateSlugs` and `hyphenate`. Each is tried against
`GET /v2/project/{slug}`.

Two guards:

- `namesCompatible` vetoes an obvious slug collision — the candidate's names must
  relate to the project's title, slug or id.
- `versionCorroborated` vetoes everything else (see below).

The reason this is not simply trusted: slugs are a shared namespace. A private
or locally built jar will happily resolve to whatever public project owns the
name.

### 4. `name` — confidence 0.60–0.90

`Resolver.byNameSearch` issues `GET /v2/search` for progressively looser
`searchQueries` (display name, mod id, launcher's name, guessed file-name id).
It deliberately does **not** filter by game version: a mod that only exists for
older releases should still be identified so the user gets "no compatible
version" rather than silence.

`rankHits` scores every hit against the candidate with `similarity`:

| Relationship | Score |
| --- | --- |
| Normalised equality | 0.90 |
| Containment ("yacl" inside "yetanotherconfiglibyacl") | 0.80 |
| 4-character-shingle Jaccard ≥ 0.75 | 0.60 + 0.80 × J |

Ties break on download count, so among equally-named candidates the popular one
wins.

The winner then faces two more gates: `trustworthyNameMatch`, which requires
either a score ≥ 0.90 or evidence that the project has published a version
plausibly matching the jar's declared version, and `versionCorroborated`.

### Corroboration

`versionCorroborated` is the single most important function in the package.

```go
func (r *Resolver) versionCorroborated(ctx, c Candidate, proj *modrinth.Project) bool {
    if !loaderPlausible(c, proj) { return false }   // hard veto
    local := c.Meta.Version
    if local == "" { return true }                   // no evidence either way
    versions, err := r.versionsCached(ctx, proj.ID)
    if err != nil || len(versions) == 0 { return false }
    // collect versions matching the normalised string or the numeric core
    if len(sameCore) == 0 { return true }            // never published it
    for _, v := range sameCore {
        if f, ok := v.PrimaryFile(); ok && sizesAgree(c.Size, f.Size) { return true }
    }
    return false
}
```

- `loaderPlausible` is a hard constraint: a Fabric jar cannot be a Forge-only
  project. This alone rejects the "MoreTools+" project, which is Forge/NeoForge
  only. It costs nothing, because the project payload already lists loaders.
- Version matching uses `normaliseVersion` (lowercase, `_`→`-`, spaces removed)
  and `numericCore` (the leading `d+(.d+)+` run), because Modrinth version
  numbers are decorated: `mc26.2-0.9.1-fabric`, `0.154.2+26.2`,
  `fabric-26.3-2.3.8`. Equality is the wrong test; containment of the numeric
  core is the right one.
- `sizesAgree` accepts a ratio up to `sizeTolerance = 2.0`. Repackaging and
  mirroring change a jar's bytes but not its size by an order of magnitude,
  whereas two genuinely different mods that share a name and version string
  differ substantially.
- If the project never published a matching version, the match is allowed
  through — that is the normal case for mirror-sourced jars where upstream
  renumbered, and there is nothing to compare.

### Persistence

`Resolver.persist` writes resolutions to the store, but **only when
`Confidence >= confNameHigh` (0.80)**. Low-confidence name matches are cheap to
recompute and would otherwise be pinned to the cache based on one weak signal.

## The migration algorithm

`internal/migrate`. `Engine.Run` is one pass that either plans or executes;
`Options.DryRun` is the only difference, and the report is identical either way.

### Deciding one mod

`Engine.decide`:

1. Title falls back through `Resolution.Title` → `Meta.Name` → the file name.
2. `Resolution.Provider == "curseforge"` → `ActionSkip`, "published on
   CurseForge only".
3. `Engine.bestVersion` queries the target's game version and loader:
   - API error wrapping `ErrNotFound` → "not published on Modrinth".
   - Empty result → `explainEmpty`, which distinguishes a missing project, an
     unknown Minecraft version, and a loader mismatch ("this mod does not
     support forge").
   - Results filtered to nothing by the channel → `excludeReason`, which names
     the channel that would work.
   - `modrinth.FilterByChannel` sorts newest-first with release winning ties.
4. No jar on the chosen version → `ActionSkip`, "upstream version has no jar".
5. Compare against the target's existing entry, `dstIndex[ProjectID]`:
   - both hashes known and equal → `ActionKeep`, "already installed".
   - both hashes known and different → `ActionReplace`. This is correct even
     when the version ids match: that combination means the local jar is a
     repackaged or stale build of the same release.
   - version ids equal but hashes unknown → `ActionKeep`, "already on this
     version". This is the mirror-sourced case, where the local jar cannot be
     hashed against upstream.
   - anything else → `ActionReplace`.
6. No entry → `ActionInstall`.

### Unknown mods

`Engine.handleUnmatched` produces `ActionUnmatched` with the reason "not found
upstream (use --copy-unknown to carry it over)", or `ActionCopy` when
`--copy-unknown` is set — the correct behaviour for a private jar or something
built from your own source.

### Duplicates

`dedupeByProject` groups by `project:<id>` (or `file:<name>` for copies) and
keeps one winner per group via `compareFreshness`: newest source version first,
then the longer destination file name (a build that bundles libraries), then
file name for determinism. Losers become `ActionDuplicate` with a reason naming
the winner and the file that would have been overwritten.

They are reported rather than dropped, because a duplicate is a symptom: the
game loads only one of the two, silently.

### Executing

`Engine.materialize`:

1. `backupReplacements` moves every file that is about to be replaced into
   `<mods>/.modharbor-backup/<YYYYMMDDTHHMMSSZ>/` (UTC, so backups sort
   chronologically across timezones). Files whose destination name equals their
   current name are skipped — there is nothing to preserve. A rename that fails
   across filesystems falls back to copy-then-remove.
2. `ActionInstall` and `ActionCopy` run concurrently in a goroutine per result,
   with the first error captured under a mutex.
3. `ActionReplace` runs afterwards, sequentially: the old file is removed if
   still present, then the new one is downloaded. A failure therefore cannot
   remove a mod before its replacement exists.
4. Identities for everything installed are written back to the cache.

Downloads (`internal/migrate/download.go`) always go through `downloadTo`:
stream to `.modharbor-dl-*` in the destination directory, `Sync`, verify
SHA-512 (prefix-normalised, so `sha512:` is tolerated), `Chmod 0644`, `Rename`.
`copyFile` uses the same temp-file pattern (`.modharbor-cp-*`) so a verbatim
copy is atomic too.

## Undoing a change: rollback

`internal/cli/rollback.go` is the reader for the layout
`internal/migrate/download.go` writes. It re-declares `backupDirName`
(`.modharbor-backup`) and the timestamp layout (`20060102T150405Z`) as local
constants, deliberately: the writer should be free to change without a
coordinated edit, and the reader must match what is actually on disk.

`loadSnapshots` lists the timestamped directories, newest first. Two buckets
are excluded — `removed/` and `duplicates/` — because those jars were
*deleted*, not replaced; restoring them would undo a removal the user asked
for. The directory name is a UTC timestamp, so a plain reverse string sort is
chronological and mtime only breaks ties within the same second.

Restoring is itself undoable, which is the interesting part. `applyRollback`:

1. sweeps **everything** currently in `mods/` into a fresh snapshot directory
   (`nextStamp` avoids colliding with a snapshot taken in the same second),
2. moves the chosen snapshot's files back.

So an interruption halfway through still leaves every jar somewhere findable,
and `rollback` can be rolled back. Where a file exists on both sides, the
snapshot wins — that is the older jar you asked for — and the newer one lives in
the new snapshot rather than being deleted.

`moveFile` falls back to copy-and-delete when `os.Rename` fails, because a
backup directory can be a symlink or a bind mount and that must not be a reason
to fail the restore.

## The `.mrpack` format

`internal/mrpack`. A `.mrpack` is a zip holding `modrinth.index.json` at its
root plus an optional `overrides/` tree.

The manifest's `dependencies` field is defined by the spec as a flat
`name → version` map, but modharbor needs richer per-file data (project id,
file name, relationship type) to build `Install`'s URL fallback. `Modpack` keeps
the rich `Dependency` type internally and projects it through a custom
`MarshalJSON`/`UnmarshalJSON` onto `wireModpack`. Entries keyed by a *file
path* are intentionally dropped on write: the format has nowhere to put them,
every exported file already carries its download URL, and the per-file mapping
only exists as a fallback for foreign packs that carry no `downloads` list.

**Export** (`export.go` + `required.go`) identifies every jar by SHA-1 *only* —
`RequiredMod.identify` deliberately does not fall back to name matching, because
guessing would quietly attach the wrong project to somebody else's jar. A
resolved mod becomes a manifest entry referencing a CDN URL, so the pack stays
small. An unresolved one is copied into the overrides tree and described by its
**local** digests, because there is no upstream to ask.

**Import** (`install.go`) prefers the pack's own `downloads` list — that is the
only source needing no network, and it is what makes an exported pack survive
the upstream project being deleted. Only when it is empty does it spend a
request, and then it batches: every missing version id goes out in one
`VersionsBatch` call rather than one call per mod. Each download is verified
against the manifest's `sha1` (the format's digest, not modharbor's usual
sha512) before being renamed into place. A file whose digest is already in
`mods/` is skipped, so re-running an import is cheap and `--yes` is safe.

Overrides are extracted by reopening the archive, because the manifest
describes them only by digest. A file already present with the expected digest is
left alone, so importing twice does not rewrite a hand-edited config.

## The state document

`internal/store/store.go`. One JSON file, `SchemaVersion: 1`, three maps:

- `Resolutions` — `sha1 → Resolution`. `Resolution` carries the provider,
  project id, slug, title, version id/number, provider filename, mod ids, the
  `Method` that produced it, the `Confidence`, the source file, and the
  instance it was seen in.
- `Cache` — `key → {expiresAt, value}`, a TTL response cache. `GetJSON` /
  `PutJSON` / `PruneExpired`.
- `Instances` — `path → InstanceRecord`, with timestamps preserved across
  partial updates.

`Save` is a no-op when the document is not dirty, and otherwise writes
temp-file-plus-`Sync`-plus-rename. A document that fails to parse is renamed to
`<path>.corrupt` and a fresh one is started: a bad cache must never be a hard
failure, but the evidence is kept.

## Reading instance metadata

Two layers, deliberately separate.

`instance` answers *what instance is this and what does it run*: loader from the
version JSON libraries, Minecraft version from arguments → sidecars → version id,
mod count, and the per-mod CurseForge ids from `TLauncherAdditional.json`.

`modmeta` answers *what is this jar*: the four descriptor formats, name
normalisation, and nested-jar discovery. It never touches the network, which is
why `deps` works with `--offline`.

The two meet in `resolver.Candidate`, which carries both a `*modmeta.Meta` and
whatever launcher metadata was available. That is what lets the corroboration
checks compare a declared loader and version against a project payload.

`readFromZip` is shared by `Read` (on disk) and `readJarBytes` (in memory), so a
nested jar is parsed by exactly the same code as a top-level one.

## Rendering

`internal/ui` is hand-rolled on the standard library — no TUI dependency, and
the requirement that `gofmt -l internal/` stays clean is easier to keep when
there is one place that knows about colour.

- `Heading` prints a blank line, a 72-cell brand bar, the bold title, a muted
  subtitle, and the bar again.
- `Table` measures visible width (ANSI escapes excluded, see `VisibleWidth`),
  shrinks the widest columns when the terminal is too narrow, right-aligns
  numeric columns, and can emit a footer rule.
- `Panel` draws the rounded box used for `Summary`, with the content column
  width equal to the widest line.
- `Task` renders one indented row: a state glyph (`add` `+`, `move` `⇢`,
  `skip` `⊘`, `warn` `▲`, `ok` `✔`, `del` `−`), a 30-column name, and a detail
  string that may itself contain coloured fragments.
- Colour is tri-state: unset auto-detects (`NO_COLOR`, `TERM=dumb`,
  `CLICOLOR_FORCE`, then is-stdout-a-tty); `--color` and `--no-color` win.
- `Prompter` degrades to defaults when stdin is not a TTY or
  `MODHARBOR_NONINTERACTIVE` is set, which is what makes every prompt safe in a
  pipe and in CI.

## Where to look when something is wrong

| Symptom | Start here |
| --- | --- |
| A jar was identified as the wrong project | `resolver.bySlug`, `resolver.byNameSearch`, `versionCorroborated` |
| The wrong version was chosen | `migrate.Engine.bestVersion`, `modrinth.FilterByChannel` |
| The wrong Minecraft version was used for an instance | `instance.Load`, `mcVersionFromSidecars`, `mcVersionFromID` |
| A jar's metadata was not read | `modmeta.Read`, `parseFabric`, `parseModsToml` |
| Something was installed that should not have been | `migrate.Engine.materialize`, `migrate/download.go` |
| A snapshot was written to the wrong place, or restore picked the wrong one | `migrate/download.go`'s `backupReplacements`, `cli/rollback.go`'s `loadSnapshots` |
| An `.mrpack` exported or imported the wrong files | `mrpack/required.go`'s `identify`, `mrpack/install.go`'s `resolveURL`, `Modpack.MarshalJSON` |
| A duplicate was reported or missed | `migrate/dedupe.go`, `cli/deps.go`'s `presentIDs` |
| Output looks wrong | `ui/table.go`, `ui/progress.go`'s `Task`, and the `render*` functions in `internal/cli` |

## Further reading

- [providers.md](providers.md) — how Modrinth and CurseForge are integrated,
  rate-limit and retry behaviour, and how to add a third.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — layout table, conventions, and
  the checklists for adding a command or a provider.