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

### [ ] B2. `add --dry-run` downloads and installs anyway
`add` declares a `--dry-run` flag but never passes it down, so the flag is
purely decorative: the jar lands in `mods/` and the command then prints
"this was a dry run". This contradicts the promise in the README.

Fix: thread `dryRun` into `installProjects`; resolve and report the plan
without downloading. Regression test asserting `mods/` is untouched.

### [ ] B3. `add` and `remove` ignore the global `-i/--instance`
`splitArgs()` returns an empty instance reference and never falls back to
`flagInstance`, so `-i` is silently dropped. Every other command uses
`pickInstanceArg` and works.

Fix: fall back to `flagInstance`, then the config default.

### [ ] B4. `update --all` runs the engine twice against a live filesystem
`update` calls `eng.Run` to compute the plan, then calls it again to apply.
The first call already applied the changes, so the second finds nothing and
reports "nothing changed" after updating four mods. Cosmetic today, but it
means the reported summary is always the empty run, and it doubles the work.

Fix: compute the plan with `DryRun: true`, then apply only the selected mods
in a single second pass.

### [ ] B5. `list --all` is documented but does not exist
The README's example block shows `modharbor list --all`; the flag is actually
`--known` (inverted sense). Either add `--all` or correct the docs.

Fix: correct the README example, since `--known` is the more useful default
for a listing command.

### [ ] B6. `search` prints only the first letter of each slug
The first column renders `h.Slug[:min(1, len(h.Slug))]`, so every row shows a
single character. Almost certainly a leftover. Either drop the column or
render the slug in full.

### [ ] B7. `export` constructs its own Modrinth client
`internal/mrpack/export.go` calls `config.Load("")` directly instead of
taking the client the app already built, so a user's configured base URL or
User-Agent is ignored, and it re-reads config from disk mid-command.

Fix: accept a `*modrinth.Client` parameter, matching `Install`.

---

## P1 — features

### [ ] B8. `watch` mode
Poll for new versions on an interval and print a notification when one
appears. Useful for a long-lived modpack where you want to know about a
release without remembering to run `outdated`.

Design: `modharbor watch [instance] --every 30m`, respects
`MODHARBOR_NONINTERACTIVE`, renders with the existing spinner and task
styles, and stops cleanly on SIGINT. Bounded by the API's rate limit, so the
interval must have a sensible minimum.

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

### [ ] B14. Test coverage for `internal/cli`
The command layer has no tests, which is how B2 through B5 went unnoticed.
`internal/cli` currently reports 0% coverage. The pure helpers
(`splitArgs`, `pickInstanceArg`, `matchMods`, `loaderName`, `projectKey`,
`orDash`, `truncateName`) are testable without a terminal; the commands
themselves want `httptest` plus a temp instance directory.

### [ ] B15. Coverage for `internal/provider/curseforge`
Currently 0%. The client is untested even though it is reachable via
`config set curseForge.apiKey`.

### [ ] B16. Tests for `internal/mrpack` install-failure paths
Export is covered. `Install` is covered for the skip case, but not for a
checksum mismatch or a truncated download, which are the paths that must not
leave partial files.

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