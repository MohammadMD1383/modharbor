# Handoff — modharbor

**Status: live at <https://github.com/MohammadMD1383/modharbor> (public).**
Read this first, then `BACKLOG.md` for the work queue.

---

## What modharbor is

A single static Go binary that keeps Minecraft mods working across game
versions. Its core job is migration: you have an old instance full of mods, a
fresh instance for a new Minecraft release, and you want the same mods working
there without doing it by hand.

```console
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --dry-run
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod
```

It already did this once, for real, on the author's instances: 33 mods in
`26.2-fabric-mod`, 0 in `26.3-fabric-mod`, 23 installed, 1 carried verbatim,
9 reported with a reason, 1 duplicate collapsed.

---

## Read these first

| Path | Why |
|---|---|
| `BACKLOG.md` | The full work queue, ordered P0–P2, with status markers |
| `internal/cli/tasks/T1.md` … `T4.md` | Task briefs: scope, constraints, verification |
| `docs/architecture.md` | Package map, identification chain, migration algorithm |
| `docs/providers.md` | Modrinth/CurseForge integration, how to add a provider |
| `README.md` | What users see first |

---

## The one thing to understand before changing anything

**Identification is the heart of the tool, and a single strategy is not
enough.**

Hashing a jar and asking Modrinth (`GET /v2/version_file/{sha1}`) is exact and
authoritative. It also fails constantly in the real world: launchers routinely
install the **CurseForge mirror** of a mod, whose bytes differ from the
Modrinth copy, so the hash lookup 404s even though the mod is right there on
Modrinth. In the author's 33-mod instance, hash lookup alone resolved 27. The
other 6 (`InventoryProfilesNext`, `common-networking`, `libIPN`,
`yet_another_config_lib_v3`, and 2 others) were all on Modrinth and only
findable by mod-id and name matching.

So `internal/resolver` tries a chain, each tagged with a confidence:

| Method | Confidence | Basis |
|---|---|---|
| `hash` | 1.00 | exact SHA-1 lookup |
| `curseforge-id` | 0.95 | launcher sidecar's CF project id, matched by name |
| `slug` | 0.90 | jar's mod id / display name probed against slugs |
| `name` | 0.60–0.90 | Modrinth search, ranked by similarity |

### The corroboration rules are not optional

Every fuzzy match is cross-checked in `versionCorroborated`:

1. **Loader veto.** A jar declaring Fabric cannot be a Forge-only project.
2. **Size check.** When the project publishes a version with the same numeric
   core, the file size must agree within 2×.

This exists because of a real bug: a private mod named `more-tools` version
`1.0.0` matched the unrelated published project *More Tools (Polymer)* `1.0.0`
— same name, same version string, 8.9 MB vs the local 2.7 MB. Migration on
that match would have installed a stranger's mod into the user's instance.

**Any change to `versionCorroborated` or `sizeTolerance` must keep
`internal/resolver/collide_test.go` passing.** Those four tests encode this
bug; treat them as the specification.

### Nested jars matter for dependencies

`internal/modmeta/nested.go` resolves libraries bundled inside other mods'
jars under `jars/`. Without it, Sodium reports seven missing Fabric API
modules and Mod Menu reports four — all false positives, all bundled inside
the very jar that needs them. There are 129 dependency edges in the author's
instance and all 129 are satisfied once nested jars are counted.

---

## Repository state

- Branch `main` tracks `origin/main`. Three worktrees are in flight.
- Three commits on `main`: the initial implementation, then the backlog and
  task briefs.
- `internal/cli` has one test file. `internal/cli` coverage was 0% when the
  backlog was written; T3 addresses it.

### Worktree workflow

```bash
git worktree list                       # see what is in flight
git worktree add ../modharbor-<name> -b <branch> main
# ... work, commit on the branch ...
git worktree remove ../modharbor-<name>  # after merging
```

Merge order matters: **T3 (test harness) branches from `main` after T1 has
been merged**, because its brief assumes T1's fixes exist.

---

## Active tasks and their file scopes

These do not overlap, which is why they run in parallel:

| Task | Branch | May touch |
|---|---|---|
| T1 — P0 CLI correctness | `fix/p0-cli-correctness` | `internal/cli/**`, `README.md` |
| T2 — `watch` mode | `feat/watch` | new `internal/cli/watch.go`, one line of `root.go` |
| T3 — CLI test harness | `test/cli-harness` | `internal/cli/**_test.go` (branches after T1) |
| T4 — progress + `--verbose` | `feat/progress-and-verbose` | `internal/cli/{download,install,add,root}.go` |

T2 and T4 both add one line to `root.go`'s `AddCommand` list. Merge conflicts
there are trivial and mechanical.

---

## Environment facts

- Go 1.23 minimum; toolchain here is much newer.
- **Only dependency is `github.com/spf13/cobra`.** No new dependencies without
  discussion. See `CONTRIBUTING.md`.
- Config: `$XDG_CONFIG_HOME/modharbor/config.json`, mode 0600 because it can
  hold an API key. State/cache: `$XDG_DATA_HOME/modharbor/state.json`.
- The author's real Minecraft instances are at
  `/home/mohammad/.minecraft/versions/26.2-fabric-mod` and `26.3-fabric-mod`,
  managed by **TLauncher**.

### Never do these in a test

A test that mutates the author's real instances destroys real data. Always:

```go
t.Setenv("MODHARBOR_MINECRAFT_DIR", t.TempDir())
t.Setenv("XDG_CONFIG_HOME", t.TempDir())
t.Setenv("XDG_DATA_HOME", t.TempDir())
```

Use `httptest.NewServer` for the API, never the live one. `internal/ui` writes
to `os.Stdout`; capture it with `ui.SetWriters(buf, buf)` where output matters.

### UI helper contract

`ui.Success`, `ui.Warn`, `ui.Info`, `ui.Note` and `ui.Failure` **both print a
line and return a string**. Call them as statements. For a coloured fragment
inside a larger expression use `ui.OK`, `ui.Bad`, `ui.InfoC`, `ui.Warnc`,
`ui.Muted`, `ui.Faint`. Calling `ui.Info` inline in an expression prints in
the wrong place — that was a real bug during development.

---

## Verification baseline

```
gofmt -l internal/ cmd/     # must be empty
go build ./...
go vet ./...
go test ./...
make check                  # fmt-check + vet + test
```

CI also runs `go test -race` on ubuntu/macos/windows across Go 1.23, 1.24 and
stable.

---

## Suggested next steps, in order

1. **Merge T1**, then re-run the verification baseline. T1 fixes four shipped
   defects, including `add --dry-run` writing files anyway.
2. **Merge T2 and T4** — independent, trivial conflicts.
3. **Start T3** off the updated `main`. The regression test for the
   swallowed-error bug (B1) is the single most valuable test to add.
4. **Push a first release.** The repo is public but has no tagged release, so
   `go install github.com/MohammadMD1383/modharbor/cmd/modharbor@latest` does
   not work yet. Tag `v0.1.0` to trigger `.github/workflows/release.yml`,
   which uses goreleaser. Before that, run `goreleaser check` locally — the
   config has only been validated as YAML, never against the schema (B17).
5. Then work down `BACKLOG.md`: B7 (`export` builds its own client), then the
   P1 features — `watch` (B8), blob cache (B9), profiles/`sync` (B10).

---

## Known rough edges

- `export` constructs its own Modrinth client instead of accepting one
  (`internal/mrpack/export.go`), so it ignores a configured base URL (B7).
- `goreleaser.yaml` references `LICENSE` and `README.md` in archives with
  wildcards; both now exist, but the nfpm section still omits docs by design
  because nfpm fails on a glob matching nothing.
- CurseForge support is implemented and tested only by construction — the
  client has no tests (B15) and is inert without an API key. Everything in the
  author's workflow works without it.