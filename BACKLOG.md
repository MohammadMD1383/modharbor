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

### [ ] B7. `export` constructs its own Modrinth client
`internal/mrpack/export.go` calls `config.Load("")` directly instead of
taking the client the app already built, so a user's configured base URL or
User-Agent is ignored, and it re-reads config from disk mid-command.

Fix: accept a `*modrinth.Client` parameter, matching `Install`.

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

### [ ] B11. Detect duplicate and shadowed mods on `scan`
`doctor` finds duplicates, but `scan` does not surface them while it already
has every jar's identity in hand. One `doctor`-style warning at the end of a
scan would save a separate command for the common case.

### [ ] B12. Respect `--loader` when the instance has none
Vanilla instances have no loader, so every command falls back to `fabric`.
Expose `--loader` as a global flag so `modharbor outdated --loader forge`
works against a modded folder with no version JSON.

### [ ] B13. Report download progress
`add` and `install` do not use the existing `ui.Progress` bar. Downloads of
50+ MB jars with no progress indication look like a hang.

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

### [ ] B19. `doctor --json` emits `{}` for every finding
`finding` has four unexported fields, so `encoding/json` writes empty objects.
Real output on a broken-jar instance:

```json
{ "instance": "d", "errors": 0, "warnings": 2, "findings": [ {}, {} ] }
```

Severity, title, detail and fix never reach the wire, so `doctor --json` is
unusable for automation — the counts are the entire machine-readable output.

### [ ] B20. `doctor --json` always exits 0
`return printJSON(rep)` short-circuits before `exitWithCode(1, nil)`, so
error-severity findings do not affect the exit status in `--json` mode while
the human path exits 1. A script gets a different verdict depending on a
formatting flag.

### [ ] B21. `projectKey` mis-parses a URL with a trailing slash *and* a query
The trailing slash is trimmed before the query string, so
`https://modrinth.com/mod/lithium/?tab=versions` yields `"lithium/"`, which
404s. Slash-before-query works; together they do not.

### [ ] B22. The better "no instance" error is unreachable dead code
`requireInstanceArg` and `missingInstanceError` are written, documented and
tested, but no command calls them, so `Execute`'s `try: modharbor instances`
hint can never fire. Users get `ResolveInstance`'s thinner message instead.
Either wire it in or delete it — T3's existing test is a signal it is wanted.

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

### [ ] B23. `SortableGameVersions []string` fails on numeric ids
`internal/provider/curseforge/curseforge.go:56,71` declare it as `[]string`,
but CurseForge sends game version **ids**, which are numbers. One such file
anywhere in a listing fails the entire decode
(`cannot unmarshal number into ModFile.sortableGameVersions.0`), so the
project reports "publishes nothing" and every sibling file is lost.

Note the inconsistency: `GameFile.GameVersions` is already `[]int` while
`ModFile.GameVersions` is `[]string`. Fix by accepting both shapes — a
tolerant unmarshaller — rather than guessing which one the API sends.

### [ ] B24. `hydrateFingerprints` copies numeric ids into `GameVersions`
Line 393: `f.GameVersions = f.SortableGameVersions` when the former is empty.
If those are ids, `Supports` compares `"432"` against `"26.3"` and rejects
every file for every Minecraft version. Ids must be resolved to names, or not
copied at all.

### [ ] B25. `Supports` silently ignores its `loader` argument
`curseforge.go:412-415` is `_ = loader`. CurseForge records loaders on the
*project*, so a Forge-only file passes a Fabric check — and the loader veto in
`internal/resolver` is exactly what stops a wrong-loader install. Resolve the
loader from the project's categories, or drop the parameter so callers cannot
rely on it.

### [ ] B26. A `null` response body decodes to a zero value with no error
Every endpoint. A phantom empty project, no error. `do` should reject `null`
explicitly.

---

## P2 — mrpack robustness (found by T7)

### [ ] B27. The temp-file-then-rename contract is not durable
`downloadInto` fsyncs the temp file but never fsyncs the containing directory
after the rename, so a crash immediately after can leave the jar missing or
zero-length even though the digest passed. `writeOverride` and `export.go`'s
`copyFile` do not fsync the file at all. The contract should be uniform.

### [ ] B28. `overrideHint` is unreachable
`resolveURL` is only called for `pack.Mods()`, whose paths start with `mods/`,
so `isOverridePath` can never be true and the hint can never print. Either
resolve overrides too, or drop the helper. (`overrideHint` is the package's
one uncovered function.)

### [ ] B29. `resolveURL` failures lose the file name
A user sees `version vvvv has no downloadable file` with no indication which
of thirty mods it refers to. `downloadInto` errors *are* wrapped with
`f.Name()`; make these consistent.

### [ ] B30. `(*Error).Error()` is context-free
`curseforge: 503 ` for a 5xx with an empty body; the project id appears only in
the `URL` field. Include the URL in the message.

### [ ] B17. `goreleaser check` in CI
The release config has been validated as YAML but never against the
goreleaser schema. Add a `goreleaser check` step, or pin the version and run
`goreleaser release --snapshot --clean` on a tag.

### [ ] B18. `--verbose` currently does nothing
The flag is parsed and never read. Either wire it to request logging or drop
it from the global flags.

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