# Handoff — modharbor

**Public at <https://github.com/MohammadMD1383/modharbor>.** 31 commits on
`main`, pushed. All 10 packages pass; `gofmt`, `go vet` clean.

Read this first, then `BACKLOG.md` for the queue and `tasks/T*.md` for briefs.

---

## What it is

A single static Go binary that keeps Minecraft mods working across game
versions. Its reason for existing is migration: an old instance full of mods,
a fresh instance for a new Minecraft release, and you want the same mods
working there.

```console
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --dry-run
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod
```

It has already done this for real on the author's instances: 33 mods in,
23 installed in `26.3-fabric-mod`, 1 copied verbatim, 9 reported with a
reason.

---

## Read these first

| Path | Why |
|---|---|
| `BACKLOG.md` | Work queue, ordered, with status markers and evidence |
| `tasks/T*.md` | Task briefs: scope, constraints, verification |
| `docs/architecture.md` | Package map, identification chain, migration algorithm |
| `docs/providers.md` | Modrinth/CurseForge integration, adding a provider |
| `docs/install.md` | Install paths, verified against the release pipeline |

---

## The one thing to understand before changing anything

**Identification is the heart of the tool, and name matching is not identity.**

Hashing a jar and asking Modrinth (`GET /v2/version_file/{sha1}`) is exact
and authoritative. It also fails constantly: launchers routinely install the
**CurseForge mirror** of a mod, whose bytes differ from the Modrinth copy, so
the hash lookup 404s even though the mod is right there. In the author's
33-mod instance, hash lookup alone resolved 27. The other 6
(`InventoryProfilesNext`, `common-networking`, `libIPN`,
`yet_another_config_lib_v3`, and 2 more) were all on Modrinth and only
findable by mod-id and name.

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
   core, file size must agree within 2×.

This exists because of a real near-miss: a private mod named `more-tools`
version `1.0.0` matched the unrelated published *More Tools (Polymer)*
`1.0.0` — same name, same version string, 8.9 MB against the local 2.7 MB.
Migrating on that match would have installed a stranger's mod.

**Any change to `versionCorroborated` or `sizeTolerance` must keep
`internal/resolver/collide_test.go` passing.** Those four tests encode the
incident; treat them as the specification.

### Nested jars matter for dependencies

`internal/modmeta/nested.go` resolves libraries bundled inside other mods'
jars under `jars/`. Without it, Sodium reports seven missing Fabric API
modules and Mod Menu four — all bundled inside the very jar that needs them.

---

## The real Minecraft instances are OFF LIMITS

**`26.3-fabric-mod` is in active play. Do not read, write, list, scan, or run
any modharbor command against it. Do not `cd` into it. Do not `ls` it.**
`26.2-fabric-mod` is the source instance and equally hands-off.

This is not hypothetical caution. Two stray jars
(`fabric-api-0.161.0%2B26.3.jar`, `sodium-fabric-0.9.3-alpha.1%2Bmc26.3.jar`)
landed in the real 26.3 `mods/` directory during a session — duplicate Sodium
plus duplicate Fabric API, which crashes Minecraft on launch. The `%2B`
URL-encoding proves they were saved from a CDN URL basename (`curl -O` style),
not by modharbor, which always uses the API-provided filename. Most likely an
agent fetched fixtures while its working directory was the real `mods/` folder.
They were removed and the instance verified clean.

Two consequences for how work is done:

- **Never count the real instances' jars as a proof of isolation.** Several
  agents correctly refused to run that check, reasoning that inspecting a live
  game folder is itself the risk. They were right. Prove isolation by
  `unshare -rm`-bind-masking the real paths, as T3 did.
- **Never `curl -O`.** It saves the URL's escaped basename. Use
  `curl -o "$SCRATCH/…"`.

Everything else happens under `/tmp`:

```bash
SCRATCH=$(mktemp -d /tmp/mh-XXXXXX)
mkdir -p "$SCRATCH/versions/testinst/mods"
# minimal testinst.json + TLauncherAdditional.json, then:
export MODHARBOR_MINECRAFT_DIR="$SCRATCH" \
       XDG_CONFIG_HOME="$SCRATCH/cfg" XDG_DATA_HOME="$SCRATCH/data"
```

In tests: `t.TempDir()` plus `t.Setenv` for `MODHARBOR_MINECRAFT_DIR` and the
three `XDG_*`. Use `httptest.NewServer` for the API, never live Modrinth or
CurseForge. CurseForge tests must use the literal `test-key`.

---

## Environment facts

- Go 1.23 minimum; toolchain here is much newer.
- **Only dependency is `github.com/spf13/cobra`.** No new dependencies without
  discussion.
- Config: `$XDG_CONFIG_HOME/modharbor/config.json`, mode 0600 (can hold an API
  key). State: `$XDG_DATA_HOME/modharbor/state.json`.
- Real instances are at `~/.minecraft/versions/{26.2,26.3}-fabric-mod`, managed
  by **TLauncher**. Off limits, including for reads.
- `internal/ui` writes to `os.Stdout`; capture with `ui.SetWriters(buf, buf)`.
  T3 noted a single-pipe capture can deadlock — `harness_test.go` now drains
  both streams concurrently.

### UI helper contract

`ui.Success`, `ui.Warn`, `ui.Info`, `ui.Note`, `ui.Failure` **both print a
line and return a string**. Call them as statements. For a coloured fragment
inside a larger expression use `ui.OK`, `ui.Bad`, `ui.InfoC`, `ui.Warnc`,
`ui.Muted`, `ui.Faint`. Calling `ui.Info` inline in an expression prints in
the wrong place — that was a real bug during development.

---

## Repository state

`main` tracks `origin/main`. Worktrees are created per task and removed after
merge:

```bash
git worktree add ../modharbor-<name> -b <branch> main
# ... work, commit on the branch ...
git worktree remove --force ../modharbor-<name>
```

Branch from **current** `main`, not an old base — T4's merge conflicted with
T1's earlier work for exactly this reason.

---

## Task history

| Task | Branch | Result |
|---|---|---|
| T1 | `fix/p0-cli-correctness` | Four shipped defects fixed; `add --dry-run` was downloading |
| T2 | `feat/watch` | `watch` command; verified live against a real instance |
| T3 | `test/cli-harness` | `internal/cli` 0% → 36.3%, `internal/ui` 0 → 11.1% |
| T4 | `feat/progress-and-verbose` | Download progress bars; `--verbose` wired |
| T5 | `fix/export-client` | B7: export uses the app's client |
| T6 | `chore/release-check` | Found the first release would have **failed** |
| T7 | `test/curseforge-mrpack` | CurseForge 0% → 99.5%, mrpack 63% → 77.3% |
| T8 | `fix/json-and-hints` | B19–B22: real JSON findings, honest exit codes |
| T9 | `fix/curseforge-decode` | B23–B26, B30: client works on real payloads |

Briefs live in `tasks/T1.md` … `T9.md`. Follow the same shape for new ones.

### What delegating actually bought

Nine of the eighteen closed items were found *by* the work rather than planned
in advance. Three worth remembering:

- **T3 refused to write tests that would have locked bugs in place.** It
  reported four defects it could not cover without editing source. All four
  were real — `doctor --json` was emitting `"findings": [{}, {}]`.
- **T7's coverage work exposed that the CurseForge client cannot decode real
  responses.** Tests written to pin behaviour found the behaviour was wrong.
- **T6's `goreleaser check` passed clean on the original config.** The release
  bug (duplicate asset upload, which GitHub rejects) only appeared from an
  actual snapshot build. Static validation was not the safety net it looked
  like.

### How to review a subagent's work

Do not take the report at face value — two agents were cut off mid-task and
left work that built and passed tests while being wrong.

- Verify with your own `gofmt`/`build`/`vet`/`test`, including `-race`.
- **Reproduce the bug claims yourself** before merging the fix.
- Check the red-test evidence by reverting each fix and confirming the test
  goes red. T9's: removing the numeric branch fails 10 tests, removing the
  null rejection fails 11.
- When a test fails, first ask whether the test is wrong. One of T9's tests
  contradicted its own fixture.
- Watch for scope creep into files other agents own.

---

## Coverage

| Package | Coverage |
|---|---|
| `internal/provider/curseforge` | 98.4% |
| `internal/store` | 83.1% |
| `internal/mrpack` | 77.3% |
| `internal/provider/modrinth` | 64.3% |
| `internal/modmeta` | 63.1% |
| `internal/migrate` | 58.9% |
| `internal/resolver` | 58.8% |
| `internal/instance` | 57.9% |
| `internal/cli` | 43.4% |
| `internal/ui` | 11.1% |

---

## Backlog: 22 of 31 closed

Remaining, in rough priority order:

| Item | What |
|---|---|
| **B27** | `downloadInto` fsyncs the temp file but not the containing directory after the rename — a crash right after can leave a jar missing despite a passing digest. `writeOverride` and `export.go`'s `copyFile` don't fsync at all. |
| **B9** | Content-addressed jar cache keyed by SHA-512, hard-linked into `mods/`. Makes repeated migrations near-instant. |
| **B10** | `sync` a saved profile — `.modharbor/profile.toml` pinning exact versions, so an install is reproducible. |
| **B11** | Surface duplicate/shadowed mods on `scan`; `doctor` finds them but `scan` already holds the identity. |
| **B12** | `--loader` as a global flag, for modded folders with no version JSON. |
| **B28** | `overrideHint` is unreachable (the package's only uncovered function). |
| **B29** | `resolveURL` failures lose the file name; `downloadInto` errors are wrapped correctly, these are not. |
| — | `add --json` emits `"installed": [{}]` — `installedMod` has only unexported fields. Needs a schema decision, so it gets its own brief. |

`BACKLOG.md` carries the full detail plus the evidence for closed items.

---

## The v0.1.0 release — READ THIS FIRST

Tag `v0.1.0` exists on `main` and a release run was dispatched
(`gh workflow run release.yml -f tag=v0.1.0`, run id `37049761783`). **Check
it first thing:**

```bash
gh run list --workflow=release.yml --limit 3
gh release view v0.1.0          # confirms artifacts landed
gh release view v0.1.0 --json assets --jq '.assets[].name'
```

Expect 11 assets: 6 archives (`_linux_amd64.tar.gz`, `_linux_arm64.tar.gz`,
`_darwin_amd64.tar.gz`, `_darwin_arm64.tar.gz`, `_windows_amd64.zip`,
`_windows_arm64.zip`), 4 linux packages (deb+rpm × amd64/arm64), and
`checksums.txt`. If the run failed, `--log-failed` and fix the cause — do not
re-tag blindly, see the traps below.

### Four bugs the release attempt exposed (all fixed, all committed)

Cutting a tag was far more productive than anything else done so far. Every
one of these had been sitting in the repo, passing locally.

1. **The CI lint job had never worked.** `--default=none` is a golangci-lint
   **v2** flag, but `version: latest` resolved a **v1.64.8** binary, which
   rejected it: `Error: unknown flag: --default`, exit 3. Every push since
   that job landed was red. Pinned to `v2.14.0`. A linter that cannot start
   is worse than no linter, because the log reads as if the code were at fault.

2. **`release.yml` was unparseable by GitHub.** Dispatching it returned
   `failed to parse workflow: (Line: 99, Col: 13): A mapping was not
   expected`. PyYAML accepted the file; GitHub's parser did not. With the
   triggers never read, GitHub listed the workflow by path instead of its
   declared name, and fired it on **every push to main**, where it failed
   immediately because `TAG` resolved to `main`. Rewritten conventionally.

   **Lesson: validate workflows with GitHub, not a YAML parser.** Any YAML
   linter will happily accept a file Actions rejects.

3. **A test asserted on deflated bytes.** `TestDoctorFixPreservesTheDuplicateItMovedAside`
   grepped a jar's raw file bytes for `"0.9.0"`, which only appears when the
   compressor happened to store the entry. Passed on Go 1.27, failed on CI's
   1.23/1.24. Now reads `fabric.mod.json` through `archive/zip`. The build gate
   caught it — the first time that file ran outside the dev machine.

4. **The tag was stale.** It was cut before fixes 1–3, so goreleaser refused:
   `git tag v0.1.0 was not made against commit a30fd8e`. The tag was moved to
   HEAD with `git tag -d` + `git push -f`. **Only safe because no release had
   been published yet** — never force-move a tag that has shipped.

### Traps for the next release

- Tag **after** the tree is green, and make sure the tag lands on the commit
  CI passed. `git push --follow-tags` or tag the pushed SHA explicitly.
- Verify the workflow parses before relying on it: `gh workflow run <file>
  -f ... --dry-run` is not available, so dispatch once with a harmless input.
- `go.devneeds.ir` (the configured GOPROXY) returns 429 under load, which
  blocks `GOTOOLCHAIN=go1.24` locally. Cross-version testing has to happen in
  CI.

---

## Suggested next steps, in order

1. **Confirm the release.** See "The v0.1.0 release" above — check the run, then
   `gh release view v0.1.0` for the 11 expected assets. If it succeeded,
   `go install github.com/MohammadMD1383/modharbor/cmd/modharbor@latest`
   finally works. Also worth watching CI on `main` go green for the first time
   in its history, which is what fix 1 above unblocked.

2. **B27** (fsync durability). It is a data-integrity issue on the download
   path, and the fix is small.

3. **B10** (`sync`). The feature people actually want when they care about a
   specific modpack configuration.

4. **B9** (blob cache). B10 first — a profile makes the cache's value obvious.

5. B11, B12, B28, B29 as convenient.

### Verify before you finish

```
gofmt -l internal/ cmd/     # must be empty
go build ./...
go vet ./...
go test ./...
make check                  # fmt-check + vet + test
```

CI runs `go test -race` on ubuntu/macos/windows across Go 1.23, 1.24 and stable.

---

## Known rough edges

- `import` downloads silently — its progress transfer would have to live in
  `internal/mrpack`, out of scope when the plumbing was added.
- `add --json` emits `"installed": [{}]` (see above).
- The CurseForge file cache has no singleflight, so concurrent misses for one
  id each spend a request. Harmless.
- CurseForge support is inert without an API key, so despite 98.4% coverage it
  has never run against the live API. Everything in the author's workflow
  works without it.