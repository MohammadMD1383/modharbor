# Handoff — modharbor

**Public at <https://github.com/MohammadMD1383/modharbor>.** `v0.1.0` is
published and all 11 expected assets landed. All 14 internal packages pass
locally; `gofmt`, `go vet` clean.

> **CI on `main` has never been green.** All 25 recorded runs failed, for three
> unrelated reasons that predate the current work: a missing `.gitattributes`
> makes `gofmt` fail on every Windows leg, the `lint` job reports 15 real
> staticcheck/`unused` findings, and a broad set of `internal/cli` tests fail on
> macOS. Do not read a red run as being about your change. See **CI is red on
> `main`** below before opening a PR.

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
| `QUEUE.md` | **Start here to pick up work.** Self-contained items, cold-pickable |
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

Note the asymmetry, because B31/B32 turn on it. A weak name match must clear
*two* gates: `versionPlausible` (has this project ever published the local
version?) and then `versionCorroborated` (do the sizes agree?). B31 fixed the
first, which had been rejecting every mc-first decoration. The second still has
the mirror-image hole — it selects the versions to size-check with
`numericCore`, so it skips the check for exactly those decorations. That is
B32, open, and deliberately not bundled into a fix that loosened something else.

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
  key). `config.Save` *enforces* that mode on an existing file rather than only
  on create — see B34. State: `$XDG_DATA_HOME/modharbor/state.json`.
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

### The branch-name trap

T10 sat on a branch called `test/version-coverage` for its whole life, and by
the end it carried twelve commits across nine packages and a dozen bug fixes.
Three separate agents flagged the mismatch before it was merged. **Name the
branch for the change, not for the first commit** — or split the work, because
nothing forces a branch to stay one thing.

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
| T10 | `test/version-coverage` (misnamed) | Ten coverage units across nine packages; 10 real defects found and fixed; B34 |

Briefs live in `tasks/T5.md` … `T9.md` (`T1`–`T4` are gone). Follow the same
shape for new ones.

### T10: coverage work is defect detection in disguise

T10 was dispatched as "pick one small unit of work" eleven times over, and every
unit was chosen as *the least-covered thing*. That heuristic kept paying, because
the uncovered code turned out to be where the bugs were. Ten real defects came
out of it, several of them load-bearing:

- `resolver`'s `versionPlausible` rejected **every** mc-first version
  decoration (B31) — the exact case fuzzy matching exists for.
- `cli/rollback.go`'s archive could not be rolled back (B33).
- `ui.InitColor`'s two explicit arms were swapped, so the call meant to
  *guarantee* colour disabled it.
- `ui`'s status helpers ate the tail of any message containing a literal `%`.
- `ui/table.go` drew every rule one cell too wide, never shrank rules on a
  narrow terminal, and asked `strings.Repeat` for a negative count.
- `config.Save` wrote an API key into a pre-existing world-readable config
  (B34) — found in round 2 and not fixed until round 10.

The lesson worth keeping: **a package at 0% is not untested code, it is
unexamined code.** Eleven rounds of "write tests for the gap" produced eleven
shipped bug fixes. The corollary is the trap — B34 sat open for eight rounds
because every agent re-derived its worklist from the coverage table instead of
reading the defects its predecessors had already reported.

### Mutation testing is how you know a test is real

T10's agents converged independently on the same discipline, and it caught real
self-deception:

- An agent's first harness scored **compile errors as catches** — 5 of 26
  "caught" mutations were just syntax errors from mangled patterns.
- An agent's size-cap test passed for the wrong reason: it forged a zip header
  claiming 64 MiB over four real bytes, so the entry was skipped by the
  *read-failure* branch and never reached the 64 MiB guard. It still passed with
  the cap raised to 4×.
- An agent asserted on `Results[0]` believing it was the survivor; results keep
  input order, so `[0]` was the duplicate. The assertion was passing for the
  wrong reason too — the fixture happened to agree with the tie-break.

When tests only *characterise* existing code there is no red phase, so the
substitute is to break the expected value and confirm the test notices and
reports the truth.

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
| `internal/hashutil` | 96.6% |
| `internal/version` | 100.0% |
| `internal/config` | 89.8% |
| `internal/resolver` | 85.0% |
| `internal/store` | 82.6% |
| `internal/mrpack` | 76.9% |
| `internal/migrate` | 74.8% |
| `internal/modmeta` | 74.8% |
| `internal/provider/modrinth` | 64.3% |
| `internal/instance` | 60.3% |
| `internal/ui` | 60.1% |
| `internal/cli` | 55.9% |

`internal/config` reads 89.8% rather than the 91.6% first reported because B34
added a `Stat`/`Chmod` branch pair; the branch is covered, the denominator grew.

---

## Backlog: 31 of 34 closed

Remaining, in rough priority order:

| Item | What |
|---|---|
| **B35** | CI on `main` has never been green — three unrelated causes. Blocks every PR. See **CI is red on `main`**. |
| **B36** | `lint` reports 15 real staticcheck/`unused` findings. Blocks every PR. |
| **B37** | `internal/cli` tests fail on macOS with genuinely wrong output. Blocks every PR. |
| **B32** | `versionCorroborated` skips the size check for mc-first version decorations. Tightens the incident's own safety net, so it needs measuring against the real instance first — see the corroboration note above. |
| **B9** | Content-addressed jar cache keyed by SHA-512, hard-linked into `mods/`. Makes repeated migrations near-instant. |
| **B10** | `sync` a saved profile — `.modharbor/profile.toml` pinning exact versions, so an install is reproducible. |

Since this was last written, B11 (duplicate warning on `scan`), B12
(global `--loader`), B27 (fsync durability), B28 (unreachable
`overrideHint`), B29 (`resolveURL` file name), B31 (`versionPlausible`
rejecting mc-first version decorations), B33 (`rollback`'s own archive was
unrestorable, and `--list --json` changed the type of `.snapshots`), B34
(`config.Save` left a pre-existing world-readable config alone), and the
`add --json` `installed: [{}]` schema have all been fixed and recorded in
`BACKLOG.md` / `CHANGELOG.md`.

T10 also found and fixed defects that never got backlog numbers because they
were not known to be broken: `ui.InitColor`'s swapped explicit arms, the `%`
mangling in `ui`'s status helpers, four box-drawing bugs in `ui/table.go`, and
a tie-break comment in `migrate` that described an mtime the code never had.
Each is covered in `CHANGELOG.md` with its red-test evidence.

### `rollback` is the recovery story — read its invariant before changing it

Every jar modharbor replaces is moved into `mods/.modharbor-backup/<stamp>/`
first, so an update that breaks the game is recoverable. A rollback then moves
the current `mods/` contents into a *fresh* snapshot before restoring, which is
what makes the restore itself undoable.

The invariant that was broken, and is now pinned by
`internal/cli/rollback_test.go`: **whatever `nextStamp` writes, `loadSnapshots`
must list and `pickSnapshot` must accept.** They were two halves of one format
and only one of them knew about the disambiguating suffix, so the archive a
rollback writes was the single directory it could not see. Any new naming in
that directory has to round-trip through a test before it ships.

`BACKLOG.md` carries the full detail plus the evidence for closed items.

---

## The v0.1.0 release — DONE

`v0.1.0` is published with all 11 expected assets: 6 archives
(`_linux_amd64.tar.gz`, `_linux_arm64.tar.gz`, `_darwin_amd64.tar.gz`,
`_darwin_arm64.tar.gz`, `_windows_amd64.zip`, `_windows_arm64.zip`), 4 linux
packages (deb+rpm × amd64/arm64), and `checksums.txt`. So
`go install github.com/MohammadMD1383/modharbor/cmd/modharbor@latest` works.

```bash
gh release view v0.1.0 --json assets --jq '.assets[].name'
```

The release shipped **while CI on `main` was red**, which is worth sitting with:
the pipeline does not gate the release on tests passing. Everything below was
found by cutting the tag, and all four are fixed.

### Four bugs the release attempt exposed (all fixed, all committed)

Cutting a tag was far more productive than anything else done so far. Every
one of these had been sitting in the repo, passing locally.

1. **The CI lint job had never worked.** `--default=none` is a golangci-lint
   **v2** flag, but `version: latest` resolved a **v1.64.8** binary, which
   rejected it: `Error: unknown flag: --default`, exit 3. Every push since
   that job landed was red. Pinned to `v2.14.0`. A linter that cannot start
   is worse than no linter, because the log reads as if the code were at fault.
   *It now starts, and reports 15 real findings — see B36.*

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
   been published yet** — never force-move a tag that has shipped. It has now
   shipped, so `v0.1.0` is frozen.

### Traps for the next release

- Tag **after** the tree is green, and make sure the tag lands on the commit
  CI passed. `git push --follow-tags` or tag the pushed SHA explicitly.
- Verify the workflow parses before relying on it: `gh workflow run <file>
  -f ... --dry-run` is not available, so dispatch once with a harmless input.
- `go.devneeds.ir` (the configured GOPROXY) returns 429 under load, which
  blocks `GOTOOLCHAIN=go1.24` locally. Cross-version testing has to happen in
  CI.
- **Make the release depend on CI.** `v0.1.0` published from a red `main`.
  Nothing in `release.yml` waits for the test matrix, so a broken tree can ship
  a binary. Worth fixing before `v0.2.0`.

---

## CI is red on `main` — B35

**All 25 recorded CI runs on `main` have failed.** This is the single most
important thing to know before opening a PR, because a red run tells you
nothing about your change. Three independent causes, all predating T10:

### B35a. `gofmt` fails on all three Windows legs

There is **no `.gitattributes`**. Windows runners check out with CRLF, so
`gofmt -l internal/ cmd/` reports nearly *every* file as unformatted:

```
::error::run 'gofmt -w internal\cli\add.go'
::error::run 'gofmt -w internal\app\app.go'
   ... one per file
```

Nothing is actually misformatted — `gofmt -l` is clean locally. The fix is a
`.gitattributes` with `*.go text eol=lf` (plus the other text types), then a
one-off renormalisation. Trivial, and it is pure noise until done, because it
masks real formatting failures on every Windows run.

### B36. `lint` reports 15 real findings

The linter now runs, and the code does not pass it. Real ones worth acting on:

- `internal/cli/update.go:153` `SA4006: this value of plan is never used` — a
  computed plan discarded. Potentially a real bug, not just lint.
- `internal/cli/version.go:56` `SA1019: cobra.ExactValidArgs is deprecated`.
- Unused symbols: `outdated.go`'s `dryRun`/`yes`/`backup` fields,
  `ui/progress.go`'s `final`, `resolve.go`'s `cacheTTL`,
  `curseforge_test.go`'s `cfRightVersions`.
- Style-only: `QF1002` tagged switch, `S1039`/`S1021`/`S1023` nits.

The `unused` struct fields are worth a look rather than a blind delete — an
unused `dryRun` on `outdated` could be a flag that was wired up and then
forgotten, which is the same class of bug as B2's decorative `--dry-run`.

### B37. `internal/cli` tests fail on macOS with wrong output

This is the one to be most careful about, because it is **not** a flake and not
a timing artifact — the tests fail with genuinely wrong values:

```
commands_test.go:147: identified = 2, want 1
commands_test.go:189: sodium-0.10.0.jar method = "name", want hash
commands_test.go:179: unknownmod-1.0.jar method = "slug", want unmatched
add_update_test.go:59: a real run downloaded nothing
hints_test.go:194: list --json with a configured default: exit 1
```

Ubuntu passes these. So something in the resolution path behaves differently on
macOS — plausibly a case-insensitive filesystem assumption, a path-separator
comparison, or a `httptest`/DNS difference. Identical failures across all three
macOS Go versions, so it is deterministic and diagnosable.

Separately, `watch_test.go` has four genuine timeouts on macOS
(`timed out waiting for the first cycle`, `timed out waiting for polling to
resume`) — those *are* timing-sensitive, and the `Test` step takes 54s.

**Diagnosis has not been attempted.** Do not guess at a fix from the log alone;
reproduce on a macOS runner first. This is a real bug that has been shipping
in plain sight, because nobody has had a green build to notice it in.

---

## Suggested next steps, in order

**`QUEUE.md` is the current, verified list** and supersedes the summary table
below. The order there is deliberate: A1 (`.gitattributes`) is nearly free and
unblocks three CI legs, A3 is a real bug already shipping to the live
Modrinth API from macOS CI, then the rest.

1. **Fix CI (B35).** Nothing else can be trusted until a run means something.
   Start with the missing `.gitattributes` (B35a) — it is nearly free and it
   stops Windows reporting every file as misformatted. Then B37, the macOS
   `internal/cli` failures, because they are real wrong-output bugs that have
   been shipping unnoticed. Then B36's 15 lint findings, checking the unused
   struct fields for forgotten wiring rather than deleting them blind.

2. **B10** (`sync`). The feature people actually want when they care about a
   specific modpack configuration.

3. **B9** (blob cache). B10 first — a profile makes the cache's value obvious.

4. **B32**, once there is a green build to measure against.

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

- **CI has never been green.** See B35. Every run has failed, for three
  unrelated reasons, so no run has ever been evidence about a change.
- The release workflow does not wait on CI, which is how `v0.1.0` shipped from
  a red `main`. Worth fixing before `v0.2.0`.
- The CurseForge file cache has no singleflight, so concurrent misses for one
  id each spend a request. Harmless.
- CurseForge support is inert without an API key, so despite ~98% coverage it
  has never run against the live API. Everything in the author's workflow
  works without it.
- `internal/ui` has dead exported surface: `Section`, `KV` and `Count` have no
  callers, and `asciiSymbols` backs a `--ascii` mode that does not exist.