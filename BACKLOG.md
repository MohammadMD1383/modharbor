# modharbor backlog

Ordered by priority. P0 items are correctness or data-safety bugs; P1 are
features other users will ask for; P2 are polish.

Status legend: `[ ]` todo, `[~]` in progress, `[x]` done.

---

## P0 — correctness and safety

### [x] B1. Error messages swallowed by `fail()`
`fail()` wrapped its message in `silentError`, and `Execute` returned the exit
code without printing `se.Err`. Every command reported failure with zero
bytes on stdout and stderr, which makes the tool unusable in CI.

Fixed: `fail()` returns a plain error; only a `silentError` with a nil inner
error is silent.

### [x] B2. `add --dry-run` downloads and installs anyway
`add` declared a `--dry-run` flag but never passed it down, so the flag was
purely decorative: the jar landed in `mods/` and the command then printed
"this was a dry run". This contradicted the promise in the README.

Fixed (T1, `b0489c2`): the flag reaches `installProjects` and gates only the
download and the write — resolution still needs the API, so a dry run still
talks to Modrinth. The summary says "would install" rather than "installed".

### [x] B3. `add` and `remove` ignore the global `-i/--instance`
`splitArgs()` returned an empty instance reference and never fell back to
`flagInstance`, so `-i` was silently dropped. Every other command uses
`pickInstanceArg` and worked.

Fixed (T1): `splitArgs` falls back to `flagInstance`, and the config default
still applies when neither is set. Positional form unchanged.

### [x] B4. `update --all` runs the engine twice against a live filesystem
`update` called `eng.Run` to compute the plan, then again to apply. The first
call already applied the changes, so the second found nothing and reported
"nothing changed" after updating four mods.

Fixed (T1): planning is `DryRun` throughout, including the interactive
re-plan after selection, with a single apply pass whose report drives the
summary.

### [x] B5. `list --all` documented but nonexistent — was a false report
The docs pass claimed the README showed `modharbor list --all`. It does not;
the command table documents `--known` and no example uses `--all`. Nothing to
fix, and `TestReadmeDoesNotDocumentListAll` now guards against it appearing.
Kept as an item because the report was wrong, not the code.

### [x] B6. `search` prints only the first letter of each slug
The first column rendered `h.Slug[:min(1, len(h.Slug))]`, so every row showed
a single character.

Fixed (T1): column removed. The title identifies the project and
`modharbor info` prints the slug when it is needed.

### [x] B7. `export` constructs its own Modrinth client
Was calling `config.Load("")` mid-command, so a configured base URL was
ignored. `Export` now takes a `*modrinth.Client` like `Install`, and
`config.Load("")` is gone from the export path. Regression test asserts a
decoy client in the XDG config receives nothing while the app's stub receives
the requests (T5, `19d83b2`).

---

## P1 — features

### [x] B8. `watch` mode
Poll for new versions on an interval and print a notification when one
appears. Useful for a long-lived modpack where you want to know about a
release without remembering to run `outdated`.

Done (T2, `feat/watch`): `modharbor watch [instance] --every <duration>`,
30m default, 60s minimum rejected rather than clamped. Prints the current
state as a baseline, then the familiar `outdated` table only when the set of
updates changes; unchanged cycles stay silent. `--json` emits one object per
cycle with a `changed` flag (a silent stream is indistinguishable from a
hang). Failed cycles warn and retry without touching the baseline; SIGINT and
SIGTERM exit 0 with a farewell. The client cache TTL is tied to the poll
interval so a release cannot hide behind a stale cache.

### [ ] B9. Cache the downloaded jars as content-addressed blobs
Every update re-downloads a jar even when the identical file is already in
`mods/` or was seen recently. A local blob cache keyed by SHA-512, with hard
links into `mods/` where the filesystem allows, would make repeated
migrations near-instant and save bandwidth.

`modharbor cache stats` and `cache clean` should follow.

### [ ] B10. `sync` a saved profile
A named profile (`.modharbor/profile.toml`) pinning exact project versions for
an instance, so an install is reproducible: `modharbor sync` brings the
instance to exactly that set, adding and removing as needed. This is what
people actually want when they care about a specific modpack configuration.

### [x] B11. Detect duplicate and shadowed mods on `scan`
`doctor` finds duplicates, but `scan` does not surface them while it already
has every jar's identity in hand. One `doctor`-style warning at the end of a
scan would save a separate command for the common case.

Done: `scan` groups rows by loader mod id via `duplicateGroups` (shared with
`diagnose`, so the two can never disagree) and prints one warning per group
plus a `run modharbor doctor --fix` hint. `scan --json` carries a
`duplicates: [{modId, files}]` array, omitted when empty. Pinned by
`scan_duplicates_test.go`: human warning, JSON payload, and quiet on a clean
instance.

### [x] B12. Respect `--loader` when the instance has none
Vanilla instances have no loader, so every command fell back to `fabric`.
`--loader` is now a global flag, so `modharbor outdated --loader forge`
works against a modded folder with no version JSON.

Done: `--loader fabric|forge|neoforge|quilt` is validated in
`PersistentPreRunE` (same error on every command) and applied in
`resolveInstance`, which covers every single-instance command; `migrate`
applies it to the target, since version selection reads the destination's
loader. `search`'s old per-command `--loader` is now the same global flag,
so its example is unchanged. Pinned by `internal/cli/loader_flag_test.go`
(override on vanilla, override wins over a detected loader, absent leaves
the instance alone, unknown rejected, normalised in place); verified live
that `outdated --json` reports `"loader": "forge"` with the flag and
`"vanilla"` without.

### [x] B13. Report download progress
`fetchFile` now takes a `*transfer` and drives the `ui.Progress` bar that
existed but was never used. `Done` is deferred immediately after the bar is
created, so every return path — including a checksum mismatch — clears the
line instead of leaving a stuck bar. Progress goes to stderr and is suppressed
under `--json`/`--quiet` (T4, `993eb1d`).

`import` downloads now report progress too: `InstallWithProgress` threads a
`ProgressFunc` hook through `downloadInto`, and the `import` command wires it
to the same `ui.Progress` bar other downloads use. The hook starts only after
the request succeeds, prefers Content-Length over the manifest size, and is
released on every return path including checksum mismatch. Silent under
`--json`/`--quiet` (T4, `993eb1d`).

---

## P2 — polish

### [x] B14. Test coverage for `internal/cli`
The command layer had no tests, which is how B2 through B6 went unnoticed.
`internal/cli` reported 0% coverage.

Done (T1 then T3): `internal/cli` 0% → 24.9% → 36.3%, and `internal/ui` from
no test files to 11.1%. The B1 regression test (a failing command must write
to stderr, not just exit non-zero) was validated by temporarily restoring the
bug and confirming three tests fail. T3 also extended the harness with a
concurrent dual-pipe drain, since the original single pipe could deadlock, and
proved isolation by running the suite inside `unshare -rm` with the real
`~/.minecraft` bind-masked.

### [x] B19. `doctor --json` emits `{}` for every finding
Was: `finding`'s unexported fields meant `encoding/json` skipped them, so
severity, title, detail and fix never reached the wire. Fixed (T8, `2ade665`)
with a `findingJSON` type and a `MarshalJSON` method, which exports the fields
without opening them to the rest of the package.
`finding` has four unexported fields, so `encoding/json` writes empty objects.
Real output on a broken-jar instance:

```json
{ "instance": "d", "errors": 0, "warnings": 2, "findings": [ {}, {} ] }
```

Severity, title, detail and fix never reach the wire, so `doctor --json` is
unusable for automation — the counts are the entire machine-readable output.

### [x] B20. `doctor --json` always exits 0
Was: `return printJSON(rep)` short-circuited before `exitWithCode(1, nil)`, so
a script got a different verdict from the same run depending on a formatting
flag. Fixed (T8): the payload is encoded first, then the exit status applied.
Verified: human and `--json` both exit 1 on an error finding.

### [x] B21. `projectKey` mis-parses a URL with a trailing slash *and* a query
Was: the trailing slash was trimmed first, so
`https://modrinth.com/mod/lithium/?tab=versions` yielded `"lithium/"`, which
404s. Fixed (T8) by stripping the query first. T8 also found a fourth missing
URL form — `http://…/project/`, which modrinth.com serves. Reverting the
ordering fails six subtests.

### [x] B22. The better "no instance" error is unreachable dead code
Was: `requireInstanceArg` and `missingInstanceError` were written, documented
and tested, but no command called them, so `Execute`'s
`try: modharbor instances` hint could never fire. Fixed (T8) by adding
`resolveInstance` as the single path every instance-taking command uses.
Which instance a command resolves is unchanged; only the error surface moved.

### [x] B15. Coverage for `internal/provider/curseforge`
Was 0%. Now 99.5% (T7, `74a2df3`): API key scoping, error mapping, malformed
and `null` bodies, 429 not retried, cache behaviour including "a 404 is not
cached", newest-file selection, fingerprint hydration, and the guarantee that
the API key never goes to a CDN host.

Writing the tests exposed four real defects in the client, now B23–B26.

### [x] B16. Tests for `internal/mrpack` install-failure paths
Now 77.3% (T7). Checksum mismatch, truncated download, lying Content-Length,
error statuses and unreachable mirrors all assert the destination is left
clean — jar absent *and* no abandoned temp file. Also pinned: a duplicated
pack entry downloads once, a stale jar is replaced then skipped, a failure
abandons the rest of the pack, and override mismatches write no config.

---

## P0 — CurseForge client cannot decode real payloads (found by T7)

These only bite once a user sets an API key, and then they bite silently:
the client reports "publishes nothing" rather than failing loudly.

### [x] B23. `SortableGameVersions []string` fails on numeric ids
Was: declared `[]string`, but CurseForge sends game version **ids**, so one
such file anywhere in a listing failed the entire decode and took every
sibling file with it.

Done (T9, `bc0cd62`): a `VersionID` type in `internal/provider/curseforge/decode.go`
accepts a number or a string and keeps the server's literal text. The ids keep
their meaning — `GameFile.GameVersions` is still `[]int`, `ModFile.GameVersions`
still names.

### [x] B24. `hydrateFingerprints` copies numeric ids into `GameVersions`
Was: `Supports` compared `"432"` against `"26.3"` and rejected every file.

Done (T9): ids resolve through CurseForge's version table, fetched lazily and
cached — a listing that already carries names spends no extra request, one that
needs it pays exactly one however many files it holds. A failed lookup is not
cached, since pinning a 500 would leave the client blind for the whole run.

### [x] B25. `Supports` silently ignores its `loader` argument
Was: `_ = loader`, so a Forge build passed a Fabric check.

Done (T9): the parameter is **removed**. Worth knowing why that is safe —
`migrate` already skips CurseForge-only projects outright
(`internal/migrate/migrate.go:392`), so a CurseForge file is never selected as
a candidate and `Supports` is never on the install path. The parameter was a
trap, not a safeguard. `Mod.SupportsLoader` now carries the loader half
honestly; it currently has no caller, which is correct given the above.

### [x] B26. A `null` response body decodes to a zero value with no error
Done (T9): `do` rejects it as `ErrNullResponse`, kept distinct from a mod that
genuinely publishes no files — the two read identically otherwise. Also folded
in B30: `(*Error).Error()` now names the URL it failed against.

---

## P2 — mrpack robustness (found by T7)

### [x] B27. The temp-file-then-rename contract is not durable
`downloadInto` fsyncs the temp file but never fsyncs the containing directory
after the rename, so a crash immediately after can leave the jar missing or
zero-length even though the digest passed. `writeOverride` and `export.go`'s
`copyFile` do not fsync the file at all. The contract should be uniform.

Fixed: new `syncDir` helper (`internal/mrpack/sync.go`) fsyncs the
destination directory after every rename; `writeOverride`, `copyFile` and
`Save` now also fsync the temp file before it. `internal/migrate`'s jar-install
path (`downloadTo`, `copyFile`) follows the same contract via its own `syncDir`,
as does `internal/cli`'s `fetchFile` via its own `syncDir`, and
`internal/store`'s `Save` via its own `syncDir`.

### [x] B28. `overrideHint` is unreachable
Was: `resolveURL` is only called for `pack.Mods()`, whose paths start with
`mods/`, so `isOverridePath` could never be true and the hint could never
print. Fixed: dropped the helper and the `%s` in the error — overrides are
extracted verbatim from the archive, never downloaded, so `resolveURL` should
never see one.

### [x] B29. `resolveURL` failures lose the file name
Was: a user saw `version vvvv has no downloadable file` with no indication
which of thirty mods it referred to. `downloadInto` errors *were* wrapped with
`f.Name()`; these were not. Fixed: `Install` wraps the `resolveURL` error the
same way, so e.g. `sodium.jar: no download URL and no Modrinth client
available`. Pinned by an assertion in
`TestInstallRefusesAnUnusablePack` (reverting the wrap fails both subtests).

### [x] B30. `(*Error).Error()` is context-free
Was `curseforge: 503 ` for a 5xx with an empty body. Done in T9: the message
names the URL it failed against.

### [x] B31. `versionPlausible` rejects every mc-first version decoration
Was: a CurseForge-mirror jar declares the bare mod version (`3.7.1`) while
Modrinth publishes the same build with the game version in front
(`mc26.2-3.7.1`) — a very common Modrinth convention. The containment name match
scores below `confSlug`, so `versionPlausible` was the gate, and it compared
against `numericCore`, which returns the *leading* dotted run. For an mc-first
number that is the Minecraft release, not the mod's version, so the gate always
said no and the mod was reported unmatched. Its own doc comment said
"containment rather than equality is the right test"; the code never did that.

Fixed: `versionPlausible` now tests containment in both directions and keeps
the numeric-core fallback. `versionCorroborated` and `sizeTolerance` are
untouched and `collide_test.go` still passes. Pinned by
`TestNameMatchSurvivesAnMCFirstVersionNumber` and the `TestVersionPlausible`
table — reverting the fix fails three tests.

### [ ] B32. `versionCorroborated` skips the size check for mc-first decorations
The other half of B31, deliberately left alone. `versionCorroborated` selects
the versions it size-checks with the same `numericCore`, so for an
mc-first-decorated project it finds no matching version, `sameCore` comes back
empty and the function allows the match through unchecked. In other words the
corroboration that B31 strengthened is skipped precisely for the decoration B31
was about: a private `More Tools 1.0.0` would sail past the published
`mc1.20.1-1.0.0` of a stranger's 8.9 MB project.

Why it was not changed in the same commit: this one *tightens* the safety net
rather than loosening it, and `collide_test.go` encodes the incident as the
specification. Widening `sameCore` to containment means every legitimate mirror
jar whose repackaged size differs by more than 2× starts being rejected — a
different trade against a different failure mode, and one that needs measuring
against the author's real 33-mod instance rather than a fixture. The loader veto
and the exact-hash path are unaffected either way.

---

## Known limitation, now fixed

`add --json` carried only counts (`installed`, `skipped`), so a script could
see that one mod was installed but not which one. Fixed: the payload now
includes a `mods` array (`name`, `version`, `file`, digests, `url`, `size`)
via an exported `installedModJSON` shape — encoding `installedMod` directly
would emit `[{}]` since its fields are unexported. `installed` stays a count
for compatibility; an empty plan encodes as `[]`, not `null`. Pinned by
`internal/cli/add_json_test.go`.

---

## P2 — release pipeline

### [x] B17. `goreleaser check` in CI
Done (T6, `4bac9c2`). `check` passes on a `release-config` CI job and as a
gate in the release workflow, and `version: latest` is now `~> v2`.

`check` alone found nothing — the real bugs only surfaced from a snapshot
build. It also found the first release would have **failed**: `release.yml`
re-uploaded assets goreleaser had already attached, and GitHub rejects
duplicate names. That step is removed. Snapshot matrix verified: 6 targets,
10 artifacts, checksums good, docs in every archive, statically linked binary
runs `version`. `docs/install.md` added. **Ready to tag `v0.1.0`**; no tag
exists yet.

### [x] B18. `--verbose` currently does nothing
Wired, not removed (T4). On stderr it reports how each jar was resolved — file,
method, project, title, confidence — plus the version and URL chosen per
project and the digests verified after download. That answers "why did
modharbor think that was X?", and the data already existed on `ScannedMod`.
Unmatched jars are reported too, since that is what answers "why is this
folder empty?"

### [x] B33. `rollback` had no tests, and its archive could not be rolled back
`internal/cli/rollback.go` is the whole recovery story — every jar modharbor
replaces is first moved into `mods/.modharbor-backup/<stamp>/` — and none of it
was tested. Seventeen functions sat at 0%.

Writing the tests found three defects, all on that path:

- **`nextStamp` and `loadSnapshots` disagreed about what a snapshot is named.**
  `nextStamp` disambiguates a same-second collision with a `-NNN` suffix;
  `loadSnapshots` gated on a bare `time.Parse(rollbackLayout, name)`, which
  rejects a suffix. The archive a rollback writes was therefore invisible to
  `--list` and unreachable by `--to` — in exactly the case `nextStamp`'s own
  comment describes, a rollback following an update inside the same second.
  Fixed by `isRestorePoint`, which strips an optional numeric suffix first. The
  suffix deliberately stays *after* the layout's trailing `Z`, since the sort is
  a reverse string compare and `-001` must read as newer than the bare stamp.
- **`rollback --list --json` changed the type of `.snapshots`.** An array when
  there is something to show, a *count* when there is not, because the empty
  case fell through to `renderNoSnapshots`'s `rollbackJSON`. A consumer
  iterating `.snapshots[]` failed on exactly the instances with nothing to
  restore. The list form now keeps its own shape.
- **`rollback --list` never printed the count.** `plural()` returns the word,
  not the phrase, and the summary interpolated it alone: `snapshots, 58 B`.

`internal/cli/rollback_test.go` covers the snapshot layer end to end — the
write-then-list round trip, `nextStamp` collision handling including its
documented give-up fallback, the bucket exclusion, newest-first ordering, the
restore/overwrite/archive split, `moveFile`'s no-partial-file guarantee, and
the command's own `--list`, `--to`, `--dry-run`, `--json` and `--quiet`
surfaces. `internal/cli` 46.0% → 55.8%; `rollback.go` is at 78.8% overall and
100% per function except the two filesystem-failure branches.

### [ ] B35. CI on `main` has never been green
All 25 recorded runs failed. Until this is fixed, a red run says nothing about
a change, and there is no build to catch anything.

**B35a — `gofmt` failed on all three Windows legs. FIXED** in `08ee195`
(PR #3): `.gitattributes` with `eol=lf` for text types. `git add --renormalize .`
was a no-op, so every tracked blob was already LF and this only governs checkout.

**B35b — the Windows test suite had never run. OPEN.** This is what B35a was
hiding. In all 25 runs the Windows `Test` step was *skipped* because the `gofmt`
step above it always failed first, so nothing about Windows was ever verified —
an absent result that reads exactly like a pass. With B35a fixed the suite runs
and **48 tests across 5 packages fail** (`cli`, `config`, `migrate`, `mrpack`,
`store`; run `37124939113`). 46 of the failures are `Access is denied` from
`syncDir`, 12 are `invalid character '▲'` in JSON output. Pre-existing, not
regressions. Full triage in `QUEUE.md` A6; the two root causes are:

- **`syncDir` calls `f.Sync()` on a directory handle, which Windows refuses**
  (`ERROR_ACCESS_DENIED`). Four copies — `store/store.go:347`,
  `migrate/sync.go:9`, `cli/sync.go:9`, `mrpack/sync.go:10` — so *every*
  atomic-write path is affected: `store.Save`, `add`, `update`, migration
  downloads, `mrpack` import/export. It runs *after* the rename succeeds, so on
  Windows the write lands and the command then exits non-zero reporting
  `Access is denied`; `store.Save` also never clears `s.dirty`. Shipped in
  `v0.1.0`. **This is the highest-value open defect in the repo.**
- **The test harness writes config to `$XDG_CONFIG_HOME`**, which
  `os.UserConfigDir()` honours only on Linux, so the config is never found and
  the CLI falls back to the live Modrinth API. Same defect as B37 below.

**B36 — `lint` reports 15 real findings.** The linter itself was fixed (it
never used to start), and now the code does not pass it:

- `internal/cli/update.go:153` — `SA4006: this value of plan is never used`. A
  computed plan discarded; possibly a real bug.
- `internal/cli/version.go:56` — `SA1019: cobra.ExactValidArgs is deprecated`.
- `internal/ui/table.go:39,45,51` — `S1023` redundant returns.
- `internal/modmeta/modmeta.go:519` — `QF1002` could use a tagged switch.
- `internal/mrpack/install_test.go:1070` `S1039`, `mrpack_test.go:347` `S1021`.
- Unused: `outdated.go`'s `dryRun`/`yes`/`backup` fields, `ui/progress.go`'s
  `final`, `resolve.go`'s `cacheTTL`, `curseforge_test.go`'s `cfRightVersions`.

The unused struct fields deserve a look rather than a blind delete: an unused
`dryRun` on `outdated` may be a flag that was wired up and then forgotten, which
is the same class of defect as B2's decorative `--dry-run`.

**B37 — `internal/cli` tests reach the live Modrinth API on macOS *and*
Windows.** Not a flake, and not the same thing as the timeouts below: Ubuntu
passes, and every macOS and Windows Go version fails identically, so it is
deterministic.

```
commands_test.go:147: identified = 2, want 1
commands_test.go:189: sodium-0.10.0.jar method = "name", want hash
commands_test.go:179: unknownmod-1.0.jar method = "slug", want unmatched
add_update_test.go:59:  a real run downloaded nothing
hints_test.go:194:      list --json with a configured default: exit 1
```

**Root cause — established, not guessed.** `internal/cli/harness_test.go:463-466`
sets only `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and
`MODHARBOR_MINECRAFT_DIR`. But `config.DefaultPaths()` resolves `ConfigDir` via
`os.UserConfigDir()`, which honours `XDG_CONFIG_HOME` **only on Linux** — on
darwin it returns `$HOME/Library/Application Support`, on Windows `%AppData%`.
`HOME`/`USERPROFILE`/`AppData` are never set. The config is therefore written
where the code will never look, `BaseURL` falls back to
`https://api.modrinth.com/v2`, and **CI makes real requests to Modrinth** on two
platforms. Every symptom follows: fixture hash lookups 404 against the real API,
real search matches what the fixture says is unknown, and the version reported
(`mc26.3-0.9.3-alpha.1-fabric`) is a *real* Sodium build that appears in no CLI
fixture.

**This also violates `HANDOFF.md`'s own rule:** *"Use `httptest.NewServer` for
the API, never live Modrinth."*

> **Superseded guess, removed.** This entry previously speculated that the cause
> was "a case-insensitive filesystem assumption, a path-separator comparison, or
> an `httptest`/DNS difference". None of those is it. Do not re-derive them.

The 12 `invalid character '▲'` JSON failures on Windows are a *symptom* of this,
not a separate encoding defect: `▲` is `ui.SymWarn` (`internal/ui/ui.go:51`), so
a warning line lands on stdout and corrupts the `--json` payload once the config
lookup fails and the live API answers differently.

Fix sketch: also set `HOME`/`USERPROFILE` to the temp home, write config to the
platform-correct location from `os.UserConfigDir()`, or thread the existing
`--config` persistent flag (`root.go:140`). Assert the fake server records
**zero** non-fixture requests — a silent return to live traffic is exactly the
failure being fixed. Do B37 and `QUEUE.md` A6b together; they are one bug.

Separately `watch_test.go` has four real timeouts on macOS (`timed out waiting
for the first cycle`, `timed out waiting for polling to resume`); those *are*
timing-sensitive, and the `Test` step runs 54s. They also fail on Windows.

Also worth fixing while in here: `release.yml` does not wait on CI, which is how
`v0.1.0` published from a red `main`. Nothing stops a broken tree shipping a
binary. Gate the release on the test matrix before `v0.2.0`.

---

### [x] B34. `config.Save` left a pre-existing world-readable config alone
`SECURITY.md` promises `config.json` is written `0600` "because the document can
hold a CurseForge or Modrinth API key. Do not relax this, and please report it if
the file is ever created world-readable." The code did not keep that promise:
`os.WriteFile` applies its perm only when it *creates* the file, so any config
that already existed kept whatever mode it had. A `0644` config — hand-created,
restored from a backup, copied off another machine, or written by an older build —
stayed `0644`, and the next `modharbor config set curseforge.apiKey …` wrote a
credential into a world-readable file.

Fixed: `Save` now tightens the mode *before* writing, so there is no window in
which the key is briefly readable, rather than chmod-ing afterwards.

The same function had a second defect, found by the same tests: it created
`Paths.ConfigDir`, but `Load` sets `Paths.ConfigFile` to the `--config` argument
while leaving `ConfigDir` at its XDG default. `--config /somewhere/else/my.json`
therefore failed with `no such file or directory` unless that directory already
existed. `Save` now creates the parent of the file it actually writes.
`configSave` in `internal/cli/cache.go` repeated the same `MkdirAll(ConfigDir)`,
which is now redundant and removed — it was additionally creating a spurious
XDG config directory whenever `--config` pointed elsewhere.

Both tests fail on the old code and pass on the new: reverting the chmod fails
`TestSaveTightensAnExistingWorldReadableConfig` (mode stays `-rw-r--r--`), and
restoring `MkdirAll(ConfigDir)` fails
`TestSaveCreatesTheParentOfAnExplicitConfigPath`. `SECURITY.md`'s claim is now
enforced rather than merely intended.

---

## Notes for future work

- The identification chain in `internal/resolver` is the heart of the tool.
  Any change to `versionCorroborated` or `sizeTolerance` needs the collision
  tests in `internal/resolver/collide_test.go` to keep passing; they encode a
  real bug where a private mod was matched to a stranger's project of the same
  name and version.
- Nested jars (`jars/*.jar` inside a mod) are resolved in
  `internal/modmeta/nested.go`. Dependency checks depend on it: without it,
  Sodium and Mod Menu report a dozen phantom missing APIs.