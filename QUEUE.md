# Work queue

Pick **one** item, implement it, commit, push. Every item below is
self-contained: you should not need to read `HANDOFF.md` or `BACKLOG.md` first.

**Rules that apply to every item:**

- **Anything you find but do not fix goes in this file.** This queue is the
  cold-pickable inventory, so an undocumented loose end is a lost one. Add the
  item *with its evidence* — the failing command, the log line, the count —
  even if it is unrelated to your change and even if you are sure someone
  already knows. The same applies to questions you could not answer and work
  you deliberately skipped. `AGENTS.md` states this rule in full.
- Branch from current `main`, name the branch for the *change* (not the first
  commit — that mistake cost `test/version-coverage` its name).
- Verify with `gofmt -l internal/ cmd/`, `go build ./...`, `go vet ./...`,
  `go test ./...`, `go test -race ./...`, `make check`. All must be clean.
- If you fix a bug, prove it was a bug **before** fixing it (reproduce it), and
  prove your test catches it **after** (revert the fix, watch it go red). Commit
  the evidence in the message.
- Tests that only characterise existing code have no red phase. Break the
  expected value and confirm a named test notices.
- Do not take a subagent's report at face value. Reproduce its claims yourself
  — one report in this repo's history got a root cause right and the
  explanation wrong.
- **The real Minecraft instances are off limits.** `~/.minecraft/versions/{26.2,26.3}-fabric-mod`
  — do not read, list, `cd` into, or run modharbor against them. Prove
  isolation with `unshare -rm` + bind-masking, not by inspecting them.
- Never `curl -O` (saves the URL's escaped basename). Use `curl -o "$SCRATCH/…"`.

---

## A. CI is red — nothing else can be trusted until these are green

**`main` has never had a green build.** All 25 recorded runs failed. The causes
are independent and none of them a flake.

**A1 is done** (commit `08ee195`, PR #3), which changed the shape of this
section: fixing it let the Windows test suite run for the first time in this
repo's history, and it immediately failed **48 tests across 5 packages**. Those
are pre-existing defects that were invisible, not regressions. See **A6**.

### A1. ~~`.gitattributes` is missing → Windows `gofmt` fails on all 3 legs~~ — DONE

Fixed in `08ee195` (PR #3). `.gitattributes` added with `eol=lf` for text types.
The `gofmt` step on all three Windows legs now passes; `git add --renormalize .`
was a no-op, so every tracked blob was already LF and this only governs
checkout. Kept here because its *consequence* is A6, not because it is open.

**Do not read A1's fix as "CI is fixed".** It repaired a check that was
reporting every file as misformatted, which had made that check unable to catch
a real formatting failure on Windows — and had been masking the entire Windows
test suite behind a `gofmt` failure.

Windows runners check out with CRLF, so `gofmt -l internal/ cmd/` reports
**every single `.go` file** as unformatted:

```
::error::run 'gofmt -w internal\cli\add.go'
::error::run 'gofmt -w internal\app\app.go'
   ... one per file
```

Nothing is actually misformatted — `gofmt -l` is clean on Linux and macOS. The
problem is that the check is meaningless on Windows, so it will mask a genuine
formatting failure there forever.

**Fix:** add `.gitattributes` with `*.go text eol=lf` (plus `*.md`, `*.yml`,
`*.yaml`, `*.json`, `*.txt`, `Makefile`), then renormalise the working tree
(`git add --renormalize .`) and commit the result.

**Verify:** after it merges, the three `windows-latest` legs get past the
`gofmt` step. They may still fail later — that is A3/A4, not a failure of this
item. Confirm the *file list* of gofmt complaints is empty, not that the leg
is green.

### A2. `lint` — 14 real findings

`golangci-lint` v2.14.0 with `--default=none --enable=govet --enable=staticcheck
--enable=ineffassign --enable=unused`. Current output, verbatim:

| Location | Finding |
|---|---|
| `internal/cli/update.go:153` | `SA4006: this value of plan is never used` |
| `internal/cli/version.go:56` | `SA1019: cobra.ExactValidArgs is deprecated` |
| `internal/cli/outdated.go:19` | `field dryRun is unused` |
| `internal/cli/outdated.go:24` | `field yes is unused` |
| `internal/cli/outdated.go:25` | `field backup is unused` |
| `internal/ui/ui.go:65` | `var asciiSymbols is unused` |
| `internal/ui/table.go:39,45,51` | `S1023: redundant return statement` (×3) |
| `internal/ui/progress.go:16` | `field final is unused` |
| `internal/resolver/resolve.go:1049` | `const cacheTTL is unused` |
| `internal/modmeta/modmeta.go:519` | `QF1002: could use tagged switch` |
| `internal/mrpack/install_test.go:1070` | `S1039: unnecessary fmt.Sprintf` |
| `internal/mrpack/mrpack_test.go:347` | `S1021: merge var decl with assignment` |
| `internal/provider/curseforge/curseforge_test.go:179` | `const cfRightVersions is unused` |

**Do these one at a time, not in one commit.** Two are worth reading before you
touch them:

- **`update.go:153` (`SA4006`) is the important one.** A computed `plan` is
  discarded. It may be a real bug rather than lint noise — read the surrounding
  function before deleting anything.
- **The `unused` struct fields are a bug class, not dead code.** `outdated`'s
  `dryRun`/`yes`/`backup` and `ui/progress.go`'s `final` could be flags that
  were wired up and then forgotten — the same defect as the old decorative
  `--dry-run` on `add` (B2). Before deleting, check whether the field's
  behaviour *should* exist. See item C1/C2 for the full analysis of
  `outcmdOptions`.

To reproduce locally, staticcheck works if you install it project-scoped:

```bash
mkdir -p .tools/bin
GOBIN="$PWD/.tools/bin" go install honnef.co/go/tools/cmd/staticcheck@latest
./.tools/bin/staticcheck -checks='U1000,SA*,S1*' ./...
```

`/.tools/` is already gitignored. Do **not** `go install` to `~/go/bin` — that
puts a binary on the user's `PATH`, outside the project. Note the pinned
`2025.1.1` cannot read Go 1.27 export data ("export data version 4 is greater
than maximum supported version 2"), so use `@latest`.

### A3. `internal/cli` tests hit the LIVE Modrinth API on macOS **and** Windows

**Highest-value item here. A real bug, already shipping.**

> **Scope correction (from the A6 triage):** this item was written as a macOS
> problem. It is not — the cause is platform-independent, and Windows is
> affected identically, plus 4 more packages beyond `internal/cli`. It is one
> defect in one helper. Treat A3 and **A6b** as a single item.

On macOS, ~10 `internal/cli` tests fail with genuinely wrong values:

```
commands_test.go:147: identified = 2, want 1
commands_test.go:189: sodium-0.10.0.jar method = "name", want hash
commands_test.go:179: unknownmod-1.0.jar method = "slug", want unmatched
commands_test.go:369: no finding explaining that Lithium has no build
add_update_test.go:59:  a real run downloaded nothing
hints_test.go:194:      list --json with a configured default: exit 1
mrpack_test.go:262:     manifest carries 0 mod file(s), want 1: []
search_test.go:30:      no row for "Continuity" in output
```

Ubuntu passes. All three macOS Go versions fail identically, so it is
deterministic, not a flake.

**Root cause (established, not guessed):** `internal/cli/harness_test.go:459`
`writeTestConfig` points the CLI at a fake `httptest` API by writing its config
to `$XDG_CONFIG_HOME/modharbor/config.json` (lines 463, 468–474). But
`config.DefaultPaths()` resolves `ConfigDir` via `os.UserConfigDir()`, and
**`os.UserConfigDir()` only honours `XDG_CONFIG_HOME` on Linux** — on darwin it
returns `$HOME/Library/Application Support`.

This is already documented in this repo. `internal/config/config_test.go:179`:

> `// os.UserConfigDir/UserCacheDir only honour XDG on Linux; elsewhere they`
> `// return the platform location, which is still the answer.`

…and that test guards its own assertion with `if runtime.GOOS == "linux"`
precisely because of it.

So on macOS the config file is never found, `BaseURL` falls back to
`config.Default()`'s **`https://api.modrinth.com/v2`** (`config.go:81`), and the
tests make **real network calls to Modrinth**. Every symptom follows:

- fixture hash lookups 404 on the real API → `method = "name", want hash`
- real search matches things the fixture says are unknown → `identified = 2, want 1`
- the version reported, `mc26.3-0.9.3-alpha.1-fabric`, is a **real** Sodium
  build — it appears in **no** CLI fixture (only `migrate/dedupe_test.go:29`
  has that string, unrelated)
- the installed jar is named by the real API, so `sodium-0.11.0.jar` (the
  fixture name) does not exist

This also violates `HANDOFF.md`'s explicit rule: *"Use `httptest.NewServer` for
the API, never live Modrinth."*

**Fix sketch.** In `writeTestConfig`, also set `HOME` and `USERPROFILE` to the
temp home (the harness currently sets only `XDG_*`, so `HOME` is the runner's
real `/Users/runner`), and write the config to the platform-correct location
derived from `os.UserConfigDir()` rather than hardcoding `$XDG_CONFIG_HOME`.
Keep `XDG_DATA_HOME`/`XDG_CACHE_HOME` as they are — `DataDir` reads
`XDG_DATA_HOME` directly so it works everywhere (`config.go:152`).

Alternative worth considering: thread the existing `--config` persistent flag
(`root.go:140`, honoured at `root.go:185`) instead of relying on discovery at
all. That is platform-independent by construction and also exercises the flag.

**Verify:** the fake server must record **zero** non-fixture requests. Add an
assertion, because a silent return to live traffic is exactly the failure being
fixed. Then confirm on a macOS leg that no test name appears that contains a
real Modrinth version string.

### A4. macOS `watch_test.go` timeouts — 5 tests, genuinely flaky

Distinct from A3: these are real timing failures, not wrong data.

```
watch_test.go:373: timed out waiting for further polling cycles
watch_test.go:407: timed out waiting for the baseline cycle
watch_test.go:415: timed out waiting for the changed cycle
watch_test.go:448: timed out waiting for the first cycle
watch_test.go:487: timed out waiting for the failure to be reported
watch_test.go:497: timed out waiting for polling to resume
watch_test.go:516: timed out waiting for two JSON cycles
```

Each fails after ~10s. `TestWatchJSONEmitsOneObjectPerCycle` appeared in one
run and not another, confirming flake rather than a deterministic break. The
`Test` step runs 54s. `watch_test.go` is untouched by recent work, so this
predates it. Likely a poll interval that is too tight for a loaded macOS
runner — lengthen the deadline rather than deleting the assertion.

### A5. `release.yml` does not wait on CI

`v0.1.0` **published from a red `main`**. Nothing in the release workflow
depends on the test matrix, so a broken tree can ship a binary. Gate the
release on the test job before cutting `v0.2.0`. (Also note `v0.1.0` now
exists and is public — never force-move a shipped tag. See `HANDOFF.md`.)

### A6. The Windows test suite has never run — 48 failures, two root causes

**This is the highest-value item in this file.** Not because it is the most
work, but because one of its two causes is a user-facing durability bug that
**shipped in `v0.1.0`** and has never been exercised on Windows at all.

**How it was found.** Every one of the 25 recorded CI runs showed `gofmt ->
failure` and `Test -> skipped` on all three `windows-latest` legs — the test
step never executed on Windows, not once. So "no Windows failures" was never a
green result; it was an absent result. A1 fixed the `gofmt` step, and the
Windows suite immediately failed:

| | |
|---|---|
| Distinct failing tests | **48** |
| Packages | **5** — `cli`, `config`, `migrate`, `mrpack`, `store` |
| Failures mentioning `Access is denied` | **46** |
| Failures mentioning `invalid character` | **12** |

Source: run `37124939113`, job `test (windows-latest, go stable)`. Linux passes.
macOS is affected by A6b but **not** by A6a — directory `Sync` works on darwin,
which is why the `Access is denied` failures are Windows-only.

These are **pre-existing defects that were hidden, not regressions** — A1's diff
adds one file and a changelog entry.

#### A6a. `syncDir` cannot work on Windows — user-facing, shipped in v0.1.0

`f.Sync()` on a *directory* handle returns `ERROR_ACCESS_DENIED` on Windows.
There are **four separate copies** of this helper, and every atomic-write path
in the tool calls one of them:

| Location | Path it guards |
|---|---|
| `internal/store/store.go:347` | `store.Save` — state.json durability |
| `internal/migrate/sync.go:9` | migration downloads |
| `internal/cli/sync.go:9` | `cli` download / `add` / `update` |
| `internal/mrpack/sync.go:10` | `mrpack` install **and** export |

So every durability-guarded write in modharbor fails on Windows:

```
add: downloading Sodium: sync C:\...\Temp\...\26.3-fabric-mod\mods: Access is denied.
update --all: sync C:\...\mods: Access is denied.
fetchFile: sync C:\...\Temp\...\001: Access is denied.
export: staging sodium-0.10.0.jar: sync C:\...\modharbor-overrides-...\mods: Access is denied.
```

**This is not cosmetic, but be precise about the symptom.** `syncDir` exists so
a crash after `os.Rename` cannot leave an entry missing (see the comment at
`store.go:344`). It is called *after* the rename has already succeeded
(`cli/download.go:169` renames, `:172` syncs the directory). So on Windows **the
write lands and then the command reports failure** — it is not data loss:

- the jar is installed, the state file is written, the `.mrpack` is exported;
- the command then exits non-zero with `Access is denied`;
- `store.Save` never reaches `s.dirty = false`, so the store stays dirty.

That is arguably worse than a clean failure, because the tool reports an error
for work it actually completed, and any caller that retries on a non-zero exit
does the work twice. The tests caught it because they assert on the returned
error; `Access is denied` mid-download is what a user sees.

**Fix sketch.** Treat a directory-sync failure as best-effort on Windows rather
than fatal — the correct behaviour is to ignore `ERROR_ACCESS_DENIED` (and
`EINVAL`, which some filesystems return for the same call) and keep the file
sync that already succeeded, since the rename is still ordered by the OS. The
four copies should be consolidated into one shared helper as part of this, so
the next platform quirk is fixed once. **Do not** simply delete the calls: on
Linux they are load-bearing.

#### A6b. `harness_test.go` writes config to a path only Linux reads

**Same root cause as A3, and A3 is not a macOS problem.** It is
platform-independent, and Windows is simply worse off. `writeTestConfig`
(`internal/cli/harness_test.go:463-466`) sets only:

```go
t.Setenv("XDG_CONFIG_HOME", ...)   // + XDG_DATA_HOME, XDG_CACHE_HOME
t.Setenv("MODHARBOR_MINECRAFT_DIR", ...)
```

but `config.DefaultPaths()` resolves `ConfigDir` through `os.UserConfigDir()`,
which honours `XDG_CONFIG_HOME` **only on Linux** — on darwin it returns
`$HOME/Library/Application Support`, and on Windows `%AppData%`. `HOME`,
`USERPROFILE` and `AppData` are never set. So the config is written to a
directory the code will never look in, `BaseURL` silently falls back to
`config.Default()`'s `https://api.modrinth.com/v2`, and **the test suite makes
real network calls to Modrinth from CI on both macOS and Windows.**

That also explains the 12 `invalid character` JSON failures, which are **not** a
BOM and are **not** an encoding problem — the offending byte is `▲`:

```
add --json is not valid JSON: invalid character '▲' looking for beginning of value
```

`▲` is `ui.SymWarn` (`internal/ui/ui.go:51`). The config is not found, the
request goes to the live API, resolution behaves differently, modharbor emits a
**warning line on stdout**, and the warning corrupts the `--json` payload. So
those 12 failures are a *downstream symptom* of A6b, not a separate defect.

**Fix sketch is in A3** and applies here unchanged — set `HOME`/`USERPROFILE` to
the temp home as well, write the config to the platform-correct location derived
from `os.UserConfigDir()`, or thread the existing `--config` persistent flag
(`root.go:140`). Add an assertion that the fake server records **zero**
non-fixture requests: a silent return to live traffic is the failure being
fixed, and this is the second time it has been invisible.

**Do A6b and A3 together, not separately.** They are one defect in one helper.

---

## B. Open backlog

### B1. `versionCorroborated` skips the size check for mc-first decorations

*(tracked as B32 in `BACKLOG.md`)*

`versionCorroborated` picks the versions to size-check with `numericCore`, which
returns the **leading** dotted run. For an mc-first-decorated project
(`mc26.2-1.0.0`) it finds no matching version, `sameCore` comes back empty, and
the match is allowed through **unchecked** — so a private `More Tools 1.0.0`
would sail past a stranger's published `mc1.20.1-1.0.0`.

**This deliberately tightens a safety net, so it is a judgement call, not a
mechanical fix.** Widening `sameCore` to containment means legitimate mirror
jars whose repackaged size differs by more than 2× start being rejected.

**Hard constraint:** `internal/resolver/collide_test.go` must keep passing —
those four tests encode a real incident where a private mod was matched to a
stranger's project. Treat them as the specification. Read the corroboration
section of `HANDOFF.md` before starting. Do not bundle this with anything else.

### B2. Content-addressed jar cache

*(B9)* Key by SHA-512, hard-link into `mods/`. Makes repeated migrations
near-instant.

### B3. `sync` a saved profile

*(B10)* `.modharbor/profile.toml` pinning exact versions so an install is
reproducible. **Do B3 before B2** — a profile is what makes the cache's value
obvious.

---

## C. Dead code and not-implemented surface

### C1. `outcmdOptions.dryRun`, `.yes`, `.backup` are dead fields

`internal/cli/outdated.go:18-26`. **Never read anywhere, never bound to a
flag.** Safe to delete — but read C2 first, because they are the same struct.

### C2. `outcmdOptions.force` and `.allowDowngrade` are read but never bound

`internal/cli/outdated.go:148-149` passes them into the plan:

```go
Force:          opts.force,
AllowDowngrade: opts.allowDowngrade,
```

…but `outdated` registers only two flags (`outdated.go:82-83`,
`--include`/`--exclude`). So both are **permanently `false`** on that path.

This is **not** a documented-flag false report, and that was worth checking:
`update` has its own local variables and *does* register `--allow-downgrade`
(`update.go:181`), `--quiet-summary` (186), `--include`/`--exclude`/`--yes`/
`--all`/`--dry-run` (180–186). `README.md:286` is accurate.

So `outcmdOptions` is **legacy**: `update` bypasses it entirely, and `outdated`
uses only the half that works. The whole struct, and possibly `planUpdates`'s
signature, can probably go. Verify `README.md` still matches afterwards —
`HANDOFF.md` records a past instance (B5) of a docs pass claiming a flag that
did not exist.

**Also check:** `doctor.go:237` and `watch.go:181` call
`planUpdates(ctx, a, inst, rows, outcmdOptions{})` with an empty struct. Once
the struct is gone, confirm those two paths still behave.

### C3. `ui.Section`, `ui.KV`, `ui.Count` are exported with zero callers

Verified: **no callers anywhere**, including tests. `Section` has no caller at
all, so its `len` vs `VisibleWidth` bug (C7) is currently unobservable.

Decide: wire them up, or delete them. Deleting is defensible — dead exported
surface in an `internal/` package is not API.

### C4. `ui.asciiSymbols` backs a `--ascii` mode that does not exist

`internal/ui/ui.go:65`. Referenced **only at its own declaration** (confirmed
by grep across all `.go` files). Either implement `--ascii` or delete the map.

### C5. `modmeta.IsLibraryOnly` always returns `false` and is never called

```go
// internal/modmeta/modmeta.go:86
func (m *Meta) IsLibraryOnly() bool { return false }
```

A hardcoded stub with zero callers. Check whether anything in the dependency
logic *should* consult it — if so, that is a missing feature, not dead code.

### C6. `LoaderModrinth` is a placeholder that can never match

```go
// internal/modmeta/modmeta.go:34
LoaderModrinth Loader = "modrinth-pack" // placeholder, never matches a jar
```

Self-documented as unreachable. Implement or remove.

### C7. `ui.Section` sizes its rule with `len(title)`, not `VisibleWidth(title)`

```go
strings.Repeat(boxH, max(4, min(Width()-4-len(title), 60)))
```

`byte` length is wrong for any title containing a multi-byte rune or an ANSI
escape — the rule comes out the wrong length. Currently unobservable (C3), so
fix it in the same item that gives `Section` a caller, or delete `Section`.

---

## D. Test gaps

Current coverage, sorted by size of hole:

| Package | Coverage | Known gap |
|---|---|---|
| `internal/cli` | 55.9% | `outdated`/`update` decision paths, `watch` |
| `internal/ui` | 60.1% | `progress.go`, `prompt.go`, `width.go` terminal probes (need a pty) |
| `internal/instance` | 60.3% | — |
| `internal/migrate` | 60.3% | — |
| `internal/provider/modrinth` | 64.3% | — |
| `internal/app` | 70.0% | — |
| `internal/modmeta` | 74.8% | `parseMcModInfo`, `fmtAny`, most of `specString` |
| `internal/mrpack` | 74.8% | — |
| `internal/resolver` | 85.0% | **`byCurseForgeID` at 9.1%** |
| `internal/store` | 82.6% | — |
| `internal/config` | 89.8% | — |
| `internal/hashutil` | 96.6% | — |
| `internal/provider/curseforge` | 98.4% | — |
| `internal/version` | 100.0% | — |

**D1. `resolver.byCurseForgeID` at 9.1% is the one to pick.** It sits at
`resolve.go:198`, immediately after the hash lookup — the `curseforge-id`
method carrying **0.95 confidence**, second-highest in the chain. It is
effectively untested on the main identification path. Note it is the method
that exists precisely for CurseForge mirror jars, which `HANDOFF.md` calls the
tool's central problem.

**D2.** `modmeta`: the `mcmod.info` legacy path (`parseMcModInfo`, `fmtAny`,
most of `specString`) is untested.

**D3.** `ui`: `width.go`'s terminal probes need a pty. Budget for that — it is
the reason `ui` sits at 60%.

**Reminder:** covering a 0% package is how T10 found ten real bugs, including
the security fix in B34. A package at 0% is unexamined code, not merely
untested code. This is a productive way to pick work — but it does not
displace A6, which is a shipped user-facing bug on Windows.

**D4. This table is Linux-only.** Every figure was measured on Linux, and
`HANDOFF.md` records that the macOS legs fail. Nothing here has ever been
measured on Windows, because the Windows suite had never executed (A6). Do not
read a coverage number as a statement about any platform but Linux.

---

## Verified-clean (do not re-investigate)

- **No `TODO`/`FIXME`/`XXX`/`HACK`** anywhere in non-test Go code.
- `go vet ./...` and `gofmt -l` are clean **on Linux**.
- CurseForge is ~98% covered but has never run against the live API (no key) —
  so treat that coverage as untested-in-anger.
- `internal/config` correctly enforces `0600` on an existing config (B34).
- `update --dry-run` is functional — it returns before applying
  (`update.go:114-119`). Not a repeat of the B2 decorative-flag bug.
- `README.md`'s documented flags all exist (`--force`, `--all`, `--known`,
  `--offline`, `--verbose`, `--json`, `--no-color`, `--quiet` all registered).

**"Verified-clean" means verified on Linux, from a Linux working tree.** It is
not a claim about macOS or Windows.