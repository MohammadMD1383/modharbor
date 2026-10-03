# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`import` now reports download progress.** Each jar pulled from a `.mrpack`
  drives the same stderr progress bar other downloads use, labelled by file
  name. Silent under `--json`/`--quiet`, and skipped files open no bar.
  Pinned by `internal/mrpack/progress_test.go` (per-file tracking, byte
  counts, `Done` on checksum mismatch, silence for skips and nil hooks).
- **`--loader` is now a global flag.** `modharbor outdated --loader forge`
  (and every other instance-taking command, plus `migrate` for its target)
  assumes that loader when the instance declares none, instead of silently
  falling back to `fabric`. Useful for a modded folder with no version JSON.
  `search --loader` keeps working: its old per-command flag is now this same
  global one.
- **`.goreleaser.yaml` is now validated on every PR.** A new
  `release-config` job in `ci.yml` runs `goreleaser check`, which validates the
  release config against goreleaser's own schema without building anything.
  Nothing else in CI ever read that file, so a typo in it previously sat
  unnoticed until somebody tagged and the release workflow failed.
- **`goreleaser check` also runs on the release gate itself**, so a bad config
  fails in the `build` job of `release.yml` instead of after the `release` job
  has already started creating a GitHub release.
- **`docs/install.md`**, documenting the real artifact matrix — which file name
  to download for each platform, what the `.deb`/`.rpm` install, how to verify
  `checksums.txt`, and which install paths modharbor does *not* provide.
- **`scan` now surfaces duplicate/shadowed mods.** A `doctor`-style warning
  per duplicate loader mod id at the end of the run, plus a `duplicates`
  array under `--json`, so the common case needs no separate `doctor` run.
- **`add --json` now names the mods it installed.** The payload carries a
  `mods` array (`name`, `version`, `file`, digests, `url`, `size`) alongside
  the `installed`/`skipped` counts; an empty plan encodes as `[]`, not `null`.

### Changed

- **goreleaser is pinned to the `~> v2` line in both workflows**, replacing
  `version: latest`. A release should be built by a known goreleaser, not by
  whatever happens to be newest on the day the tag is pushed.
- **The `.deb`/`.rpm` packages now ship their documentation.**
  `LICENSE`, `README.md` and `CHANGELOG.md` are installed under
  `/usr/share/doc/modharbor/`, and the packages declare `license: MIT`. The
  comment claiming this was waiting on a README and a LICENSE was stale: both
  have been tracked in git for a while.
- **Archive file globs are exact filenames instead of `LICENSE*`-style
  patterns.** A wildcard silently tolerates a missing file, which is how an
  archive ends up shipping without its `LICENSE`; nfpm's `contents` are exact
  for the same reason, since nfpm hard-fails on a glob that matches nothing.

### Fixed

- **`release.yml` no longer re-uploads the release assets.** goreleaser already
  creates the GitHub release *and* uploads every one of its artifacts to it —
  the six archives, the four linux packages and `checksums.txt`. The extra
  `softprops/action-gh-release` step pointed at those same paths, so it could
  only ever re-upload files that already existed on the release, which GitHub
  rejects. The first tagged release would have ended with a published release
  and a red workflow run.
- **Removed the stale `README.md` install-section note.** Windows release
  artifacts are `.zip` while every other platform is `.tar.gz`, and the README
  already says so (`tar.gz` (`zip` on windows)); the `Known issues` entry
  predated that fix.
- **Durable installs: fsync the file and the containing directory.** `mrpack`
  (`downloadInto`, `writeOverride`, `copyFile`, `Save`), `migrate`
  (`downloadTo`, `copyFile`), `cli` (`fetchFile`) and `store` (`Save`)
  now fsync the temp file before rename and the
  destination directory after it, so a crash right after a passing digest
  cannot leave a jar missing or zero-length.
- **Failed `.mrpack` downloads now name the file.** A `resolveURL` failure is
  wrapped with the entry's file name, the same way `downloadInto` errors
  already were, instead of reporting a bare version with no indication which
  of thirty mods it referred to.
- **Removed the unreachable `overrideHint`.** `resolveURL` only ever sees
  `mods/` entries, never overrides, so the override branch could never print;
  the error no longer carries its `%s`.

## [0.1.0] - 2026-10-02

First public release. modharbor identifies the mods in an instance, finds the
newest build compatible with a target Minecraft version and loader, verifies
the download, and installs it — with a dry-run plan, a backup of everything it
replaces, and a written reason for every mod it declines to move.

### Added

**Commands**

- `migrate <source> <target>` — the primary workflow. Identifies every jar in
  the source instance, resolves the newest build for the target's Minecraft
  version and loader, downloads, verifies, and installs. `--dry-run`,
  `--copy-unknown` (carry private/local jars verbatim), `--with-configs` (also
  copy `config`, `defaultconfigs`, `resourcepacks`, `shaderpacks`, never
  clobbering existing directories), `--allow-downgrade`, `--include`,
  `--exclude`, `--version-channel`, `--yes`, `--quiet-summary`.
- `update` — update an instance in place, interactive by default with a
  multi-select over available updates; `--all`, `--dry-run`, `--allow-downgrade`,
  `--include`, `--exclude`, `--yes`, `--quiet-summary`.
- `outdated` — list what has a newer build for this exact game version and
  loader.
- `scan` — identify every jar, show the method and confidence of each match.
  `--force` to re-resolve, `--all` to include unmatched rows inline.
- `list` (`ls`) — installed jars with resolved identity, version, size and mod
  id. `--known` hides unmatched jars, `--force` re-resolves.
- `add <project...>` — install by slug, project id or full Modrinth URL, with
  `required` dependencies followed recursively. `--channel`, `--no-deps`,
  `--dry-run`, `--yes`.
- `remove <mod...>` (`rm`, `uninstall`) — forgiving name/mod-id/file-name
  matching. `--keep-files` moves jars to `.modharbor-backup/removed/` instead
  of deleting them.
- `info <project>`, `search <query>` (`-n/--limit`, `--loader`, `--category`,
  `-s/--sort`) — Modrinth catalogue, filtered to the in-scope instance's game
  version and loader so results are installable as-is.
- `rollback [instance]` — restore the jars the last `migrate` or `update`
  replaced. `--list` enumerates snapshots with their sizes and ages, `--to
  <stamp>` selects a specific one (exact match, not prefix, so you get the one
  you can see), `--dry-run` plans, `--yes` skips the prompt. Restoring is itself
  undoable: the current `mods/` contents are swept into a fresh snapshot before
  anything is put back, so an interrupted rollback still leaves every jar
  somewhere findable and a rollback can be rolled back. The `removed/` and
  `duplicates/` buckets are deliberately not restore points — those jars were
  removed on request — and the command says so rather than looking empty.
- `export <dir> [instance]` — write an instance as a Modrinth `.mrpack`. Each
  jar is identified by SHA-1 *only*; a resolved mod is referenced by CDN URL and
  pinned to the exact installed version, and an unresolved one is embedded under
  `overrides/` with its real digests, so the pack always round-trips. Config,
  resourcepack and shaderpack directories are packaged too
  (`--include-overrides`), staged through a scratch directory that is removed
  afterwards. `--offline` is refused rather than silently turning every mod
  into an override. `--name` overrides the pack name.
- `import <file.mrpack> [instance]` — install a `.mrpack`. The pack's own
  `downloads` list is preferred because it needs no network and survives the
  upstream project being deleted; only packs without one spend a request, and
  then in a single batched call. Every file is verified against the manifest's
  `sha1` before it is renamed into place, a digest already present is skipped so
  re-running is cheap, and `overrides/` is extracted to the instance root
  without clobbering a file that already matches. `--no-overrides`, `--dry-run`,
  `--yes`.
- `link` / `unlink` (`unpin`) — pin a jar to a project by hand. Manual links are
  authoritative: automatic resolution never overwrites them.
- `doctor` — the checks that actually cause Minecraft to fail to start with
  mods: unsupported game version, missing required dependency, two jars
  providing the same mod id, a jar left over from another Minecraft release,
  and no detectable loader with mods installed. `--fix` moves duplicate jars
  into `.modharbor-backup/duplicates/` rather than deleting them. Exits 1 when
  errors are found.
- `deps` — declared dependencies read from each jar's own metadata, so it works
  offline. `--missing` shows only unsatisfied references.
- `cache` (`state`) — `info`, `path`, `clear`, `prune`, `forget <sha1>`.
- `instances` (`profiles`), `config` (`list`, `get`, `set`, `path`),
  `completion <shell>`, `version`.

**Identification**

- A four-strategy chain, strongest first, each tagged with a confidence:
  `hash` (SHA-1 → `GET /v2/version_file/{sha1}`, 1.00), `curseforge-id` (the
  project id the launcher recorded, 0.95), `slug` (mod id / display name probed
  against Modrinth slugs, 0.90), and `name` (search ranked by similarity,
  0.60–0.90). The chain exists because launchers frequently install the
  CurseForge mirror of a mod: the bytes differ, so the hash lookup 404s even
  though the mod is on Modrinth.
- Corroboration of every fuzzy match: a hard loader veto (a jar declaring Fabric
  cannot be a Forge-only project), version agreement on the normalised string or
  its leading dotted-numeric core, and a file-size tolerance of 2× when a
  same-version build exists. Without this a private "More Tools" 1.0.0 jar
  matches an unrelated published "More Tools (Polymer)" 1.0.0 and migration
  installs a stranger's mod.
- Camel-case-aware slug derivation, so `YetAnotherConfigLib`, `libIPN` and
  `WorldEditCUI` become `yet-another-config-lib`, `libipn` and
  `world-edit-cui` without shredding acronyms.
- Resolutions are cached by SHA-1 in `state.json`. Results below 0.80
  confidence are deliberately **not** cached, so a weak match is re-checked when
  more context exists.
- `link` writes a `manual` resolution that short-circuits the chain.

**Safety**

- Every download is streamed to a temporary file in the destination directory
  and renamed into `mods/` only after its SHA-512 matches. A failed or truncated
  download never leaves a broken jar where the loader would try to read it.
  Renaming also falls back to copy-then-remove when a backup crosses a
  filesystem boundary.
- Replaced jars are moved to `<mods>/.modharbor-backup/<UTC timestamp>/`
  before anything is installed, so an interrupted migration is recoverable —
  and `rollback` puts them back without a manual `mv`.
- `install` and `copy` actions are applied concurrently and only then are
  replacements applied, one at a time, so a failure can never remove a mod
  before its replacement is on disk.
- Duplicate jars are collapsed by `dedupeByProject`, which keeps the newest
  source version, reports the rest with the file that would have been
  overwritten, and never drops them silently. Two copies of Fabric API in one
  folder is the canonical case.
- `--dry-run` on every mutating command; every prompt is gated on
  `ui.IsInteractive()`; `--json` never prompts.

**Discovery and metadata**

- Instance discovery from the vanilla `<root>/versions/<id>/` layout, with
  loader detection from the version JSON's libraries (`net.fabricmc:fabric-loader`,
  `org.quiltmc:quilt-loader`, `neoforged`, `net.minecraftforge`) and `mainClass`
  as a fallback.
- Minecraft version resolution from the version JSON arguments, then launcher
  sidecars (`TLauncherAdditional.json`, `mmc-pack.json`, `instance.cfg`,
  `ATLauncher.cfg`), then the version id — so instances named
  `26.3-fabric-mod` are understood rather than guessed at.
- `instance.ParseTLauncherMods` reads the per-mod CurseForge project ids and
  launcher's own SHA-1 out of `TLauncherAdditional.json`, which is what makes
  strategy 2 possible.
- Jar metadata reading for `fabric.mod.json`, `quilt.mod.json`,
  `META-INF/mods.toml` and legacy `mcmod.info`, including NeoForge's two-level
  `[dependencies.<modid>.<id>]` form and `breaks` mapped to conflicts.
- Mods bundled *inside* another mod's jar (`jars/`, `META-INF/jars/`) are read
  and counted as installed, with a 64 MiB cap per nested entry. Without this,
  Sodium's bundled Fabric API modules and Mod Menu's screen APIs are reported
  as a long list of phantom missing dependencies.
- `GuessModIDFromFileName` handles launcher junk in jar names: percent-escapes
  (`sodium-fabric-0.9.2%2Bmc26.2.jar`) are decoded, and loader/version noise
  tokens (`fabric`, `forge`, `mc`, `shaded`, …) are stripped without eating
  legitimate id parts like `fabric-api` or `libipn`.

**Providers**

- Modrinth API v2 client: hash lookup, project, version listing, search with
  facets, batched version fetch, and CDN URL resolution from the `/data/<hash>`
  mirror path.
- Optional CurseForge API v1 client. Without a key the client refuses to make
  requests rather than failing with confusing 403s, and modharbor is fully
  functional via Modrinth; CurseForge-only mods are reported as such instead of
  being probed with a numeric id Modrinth will never answer.
- Every provider request retries with exponential backoff and honours
  `Retry-After` on 429, and responses are cached in memory for the run.

**Configuration**

- `$XDG_CONFIG_HOME/modharbor/config.json`, written with mode `0600` because it
  can hold API keys. `config list` prints the effective settings.
- `$XDG_DATA_HOME/modharbor/state.json` for cached identities, API responses and
  instance records. Written atomically via temp file plus rename.
- Global flags: `--config`, `--minecraft`, `--instance/-i`, `--json`,
  `--no-color`, `--color`, `--quiet/-q`, `--verbose/-v`, `--offline`,
  `--channel`.

### Changed

- **`version_type` is filtered client-side, not server-side.** Modrinth's
  `version_type` query parameter accepts only a single value, so asking for
  `beta` would also exclude releases, and asking the server to filter would
  hide exactly the pre-release builds a user needs to be told about. The
  parameter is no longer sent; `FilterByChannel` and `TypeRank` filter and rank
  client-side, so a `beta` channel means "releases and betas, newest first,
  releases winning ties".
- Skip reasons are specific rather than a blanket "no compatible version":
  `only a pre-release build exists for MC 26.3 — retry with --channel beta`,
  `only an experimental build exists for MC 26.3 — retry with --channel alpha`,
  `not published on Modrinth`, `this mod does not support forge`,
  `no fabric build for MC 26.3`, `published on CurseForge only; install it from
  the CurseForge app or launcher`.
- A hash match whose version does not support the instance's Minecraft version
  is still kept — it is useful for migration — but its confidence is lowered from
  1.00 to 0.95 so the flag is visible in `scan`.
- `outdated` and `doctor` derive their plan from one dry-run pass of the
  migration engine, so version selection has exactly one implementation.

### Fixed

- **Scalar `license` and `authors` in `fabric.mod.json`.** The spec allows
  `"license": "MIT"` as well as `"license": ["MIT", "Apache-2.0"]`, and
  `"authors"` as a bare string as well as an array of objects. A strict decoder
  rejected the singular forms, which made a perfectly good mod look
  unidentifiable and dropped it out of migration entirely. Both fields now use
  tolerant unmarshalers (`flexStringList`, `flexAuthors`) that also accept
  string→object forms, and the `authors` hook lives on the slice type rather
  than its element — `encoding/json` rejects a scalar before it ever reaches an
  element unmarshaler.
- **Unexpected types elsewhere in `fabric.mod.json` no longer cost a mod its
  identity.** If the full decode fails, `parseFabricMinimal` reads only the
  scalar fields needed for identification (`id`, `name`, `version`,
  `description`) and recovers dependencies best-effort.
- **NeoForge's two-level dependency form** (`[dependencies.worldedit.incompatible]`)
  was being read as a plain dependency. It is now mapped to conflicts, and
  `mandatory = false` maps to recommendations.
- **Duplicate jars were both being carried across.** Two copies of the same
  project resolved to the same destination file name; `dedupeByProject` now
  keeps the newest source version and reports the rest.
- **Percent-escapes in launcher-downloaded jar names.** `%2B` and `%20` are
  decoded before a mod id is guessed, so
  `sodium-fabric-0.9.2%2Bmc26.2.jar` identifies as `sodium`.
- **A corrupt `state.json` blocked every command.** It is now moved aside to
  `state.json.corrupt` and a fresh document is started, because a bad cache
  must never be a hard failure.
- **Metadata reads are size-capped** (4 MiB per descriptor entry, 64 MiB per
  nested jar) so a hostile or malformed archive cannot exhaust memory.

[Unreleased]: https://github.com/MohammadMD1383/modharbor/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/MohammadMD1383/modharbor/releases/tag/v0.1.0