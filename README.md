# modharbor

**A safe harbor for your Minecraft mods between game versions.**

modharbor is a single static Go binary that identifies every jar in your
`mods/` folder, finds the newest build of each mod that is compatible with the
Minecraft version and loader of a target instance, verifies the download, and
installs it — with a plan you can inspect first and a backup of everything it
touches.

---

## Why

Every Minecraft release, you do the same tedious thing by hand: create a fresh
instance, open each of the 30+ jars you had, work out what mod it is and which
project it came from, check whether that mod has shipped a build for the new
release, download the right file, and drop it in. Repeat. It is a lot of
clicking and a lot of chances to grab the wrong jar.

modharbor automates exactly that, and the interesting part is *knowing what each
jar is*.

Hashing a jar and asking Modrinth is exact — but it fails constantly in the
real world. Launchers frequently install the **CurseForge mirror** of a mod.
Those bytes are not the Modrinth copy, so the SHA-1 lookup 404s even though the
mod is right there on Modrinth. In the author's own 33-mod instance, hash lookup
alone resolved 27; the other six were all on Modrinth, but only findable by the
mod id or the display name.

So modharbor tries a **chain of increasingly fuzzy strategies**, each tagged
with a confidence score, and corroborates every fuzzy match against the version
the jar declares and the size of the file. See
[How identification works](#how-identification-works).

---

## What it looks like

`modharbor scan` on a 20-mod 26.2 Fabric instance. Note `Iris Shaders`: that jar
came from a CurseForge mirror, so its hash misses and it is matched on its mod
id instead. `Frobnitz` is a private jar that nobody publishes.

```console
$ modharbor scan 26.2-fabric-mod
────────────────────────────────────────────────────────────────────────
  modharbor scan
  26.2-fabric-mod (MC 26.2, Fabric)  ~/.minecraft/versions/26.2-fabric-mod/mods
────────────────────────────────────────────────────────────────────────

  MOD                     VERSION               SOURCE  MATCH
  ────────────────────────────────────────────────────────────
  AppleSkin               3.0.10+mc26.2         hash     100%
  BetterF3                19.0.0                hash     100%
  Cloth Config API        26.2.155+fabric       hash     100%
  Continuity              3.0.1+26.2            hash     100%
  Controlling             26.2.4                hash     100%
  Dark Loading Screen     1.6.19                hash     100%
  Entity Culling          1.11.2                hash     100%
  Fabric API              0.152.1+26.2          hash     100%
  Fabric API              0.161.0+26.2          hash     100%
  Fabric Language Kotlin  1.14.1+kotlin.2.4.20  hash     100%
  FerriteCore             9.0.0-fabric          hash     100%
  ImmediatelyFast         1.16.5+26.2-fabric    hash     100%
  Iris Shaders            1.11.4+26.2-fabric    mod id    90%
  Lithium                 mc26.2-0.25.3-fabric  hash     100%
  Mod Menu                20.0.3                hash     100%
  More Culling            1.8.1                 hash     100%
  Shulker Box Tooltip     5.4.1+26.2-fabric     hash     100%
  Sodium                  mc26.2-0.9.2-fabric   hash     100%
  Zoomify (Zoom)          2.16.3+26.2           hash     100%

▲ 1 mod(s) could not be matched to an upstream project
  ⊘ Frobnitz Local Tweaks         left as-is; carried across verbatim on migrate

  └─ pass --all to see them inline, or `modharbor link <sha1> <project>` to map one by hand
✔ identified 19 of 20 mods
```

Now carry those 20 mods into a fresh 26.3 instance — **without writing
anything**:

```console
$ modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --dry-run
────────────────────────────────────────────────────────────────────────
  Migration plan
  26.2-fabric-mod (MC 26.2, Fabric)  →  26.3-fabric-mod (MC 26.3, Fabric)
────────────────────────────────────────────────────────────────────────
  + AppleSkin                     3.0.10+mc26.3  appleskin-fabric-mc26.3-3.0.10.jar
  + Cloth Config API              26.3.159+fabric  cloth-config-fabric-26.3.159.jar
  + Continuity                    3.0.1+26.3  continuity-3.0.1+26.3.jar
  + Controlling                   26.3.3  Controlling-fabric-26.3-26.3.3.jar
  + Dark Loading Screen           1.6.20  dark-loading-screen-1.6.20.jar
  + Entity Culling                1.11.2  entityculling-fabric-1.11.2-mc26.3.jar
  + Fabric API                    0.161.0+26.3  fabric-api-0.161.0+26.3.jar
  + Fabric Language Kotlin        1.14.1+kotlin.2.4.20  fabric-language-kotlin-1.14.1+kotlin.2.4.20.jar
  + FerriteCore                   9.0.0-fabric  ferritecore-9.0.0-fabric.jar
  + ImmediatelyFast               1.17.1+26.3-fabric  ImmediatelyFast-Fabric-1.17.1+26.3.jar
  + Iris Shaders                  1.11.7+26.3-fabric  iris-fabric-1.11.7+mc26.3.jar
  + Lithium                       mc26.3-0.26.2-fabric  lithium-fabric-0.26.2+mc26.3.jar
  + Mod Menu                      21.0.0  modmenu-21.0.0.jar
  + Shulker Box Tooltip           5.4.2+26.3-fabric  shulkerboxtooltip-fabric-5.4.2+26.3.jar
  + Sodium                        mc26.3-0.9.2-fabric  sodium-fabric-0.9.2+mc26.3.jar
  + Zoomify (Zoom)                2.16.3+26.3  zoomify-2.16.3+26.3.jar
  ⊘ BetterF3                      no fabric build for MC 26.3
  ⊘ More Culling                  only a pre-release build exists for MC 26.3 — retry with --channel beta
  ▲ Fabric API                    same mod as Fabric API; would overwrite fabric-api-0.161.0+26.3.jar
  ▲ Frobnitz Local Tweaks

  ╭  Summary ────────────────────╮
  │   16  to install          │
  │    2  no compatible build │
  │    1  duplicate jars      │
  │    1  unmatched upstream  │
  ╰───────────────────────────╯

• dry run: nothing was written
  └─ re-run without --dry-run to apply
```

Read the glyphs: `+` install, `⇢` replace, `⊘` skip, `▲` needs a decision.
`16 + 2 + 1 + 1 = 20`, one row per jar in the source folder. The two `⊘` rows
each say exactly *why* there is nothing to install, and the `▲` rows are the
two things only you can decide.

Run it for real and the same plan is applied, then the leftovers are spelled
out:

```console
$ modharbor migrate 26.2-fabric-mod 26.3-fabric-mod
────────────────────────────────────────────────────────────────────────
  Migration complete
  26.2-fabric-mod (MC 26.2, Fabric)  →  26.3-fabric-mod (MC 26.3, Fabric)
────────────────────────────────────────────────────────────────────────
  + AppleSkin                     3.0.10+mc26.3  appleskin-fabric-mc26.3-3.0.10.jar
  … (the other 15 installs) …
  ⊘ BetterF3                      no fabric build for MC 26.3
  ⊘ More Culling                  only a pre-release build exists for MC 26.3 — retry with --channel beta
  ▲ Fabric API                    same mod as Fabric API; would overwrite fabric-api-0.161.0+26.3.jar
  ▲ Frobnitz Local Tweaks

  ╭  Summary ────────────────────╮
  │   16  to install          │
  │    2  no compatible build │
  │    1  duplicate jars      │
  │    1  unmatched upstream  │
  ╰───────────────────────────╯

▲ 1 duplicate jar(s) — only one copy of each mod can load
  ▲ Fabric API                    same mod as Fabric API; would overwrite fabric-api-0.161.0+26.3.jar

▲ 3 mod(s) need a manual decision
  ⊘ BetterF3                      no fabric build for MC 26.3
  ⊘ More Culling                  only a pre-release build exists for MC 26.3 — retry with --channel beta
  ⊘ Frobnitz Local Tweaks         no upstream match — re-run with --copy-unknown to carry it over
```

After that, keeping the instance current is one command:

```console
$ modharbor outdated 26.3-fabric-mod
────────────────────────────────────────────────────────────────────────
  Update check
  26.3-fabric-mod (MC 26.3, Fabric)
────────────────────────────────────────────────────────────────────────

     MOD       INSTALLED             AVAILABLE                 SIZE
  ──────────────────────────────────────────────────────────────────
  ⇢  Lithium   mc26.2-0.25.3-fabric  mc26.3-0.26.2-fabric  914.5 kB
  ⇢  Mod Menu  20.0.3                21.0.0                856.8 kB
  ⇢  Sodium    mc26.2-0.9.2-fabric   mc26.3-0.9.2-fabric     1.9 MB
  ──────────────────────────────────────────────────────────────────
     3         to update

  └─ run `modharbor update` to install these
```

---

## Install

```sh
go install github.com/MohammadMD1383/modharbor/cmd/modharbor@latest
```

Requires Go 1.23 or newer. The only dependency is
[cobra](https://github.com/spf13/cobra).

Prebuilt static binaries (`CGO_ENABLED=0`) for linux, darwin and windows on
amd64/arm64 are attached to
[GitHub Releases](https://github.com/MohammadMD1383/modharbor/releases) as
`tar.gz` (`zip` on windows), plus `.deb`/`.rpm` for linux and a
`checksums.txt`; download one, put it on your `PATH`, and check it:

```console
$ modharbor version
modharbor 0.1.0
  built:    2026-10-02T15:04:05Z
  runtime:  go1.23.0 linux/amd64
```

modharbor finds your Minecraft directory by itself (it probes `~/.minecraft`,
`~/.local/share/minecraft`, the macOS and CurseForge locations, and
MultiMC/Prism instance roots). If it guesses wrong:

```sh
modharbor config set minecraftDir ~/Games/minecraft
```

---

## Quick start

The scenario modharbor exists for: you have a working instance full of mods,
you made a fresh instance for a new Minecraft release, and you want the same
mods working there.

```sh
# 1. See what would happen. Writes nothing.
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --dry-run

# 2. Read the plan. Then do it.
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod

# 3. Carry your private jars across too, and your configs.
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --copy-unknown --with-configs
```

Because you are on a terminal, step 2 shows you the plan and asks for
confirmation before touching anything. Add `--yes` to skip the prompt, or
`--quiet-summary` in a script.

Then keep it current:

```sh
modharbor outdated            # what has a newer compatible build
modharbor update              # pick and apply interactively
modharbor doctor              # why Minecraft refuses to start
modharbor rollback            # changed your mind
```

### Sharing a pack

Once you have a working instance, `export` writes it as a Modrinth
`.mrpack` — pinned to the exact versions you have right now, not to whatever is
newest, so importing it elsewhere reproduces what you actually play:

```console
$ modharbor export ./packs 26.2-fabric-mod
────────────────────────────────────────────────────────────────────────
  Export
  26.2-fabric-mod (MC 26.2, Fabric)  →  packs/26.2-fabric-mod-26.2.mrpack
────────────────────────────────────────────────────────────────────────

  ✔ 26.2-fabric-mod               26.2
  ✔ 18 mod(s)                     resolved by download URL
  ▲ 2 override(s)                 no upstream match — carried verbatim

  ╭  Summary ─────────────────────────────────────╮
  │   18  referenced by URL                    │
  │    2  shipped as overrides                 │
  │     —  configs and resource packs included │
  │   20  files in the manifest                │
  ╰────────────────────────────────────────────╯

✔ wrote packs/26.2-fabric-mod-26.2.mrpack
2 mod(s) had no Modrinth match and were embedded in the pack
  └─ restore it anywhere with `modharbor import 26.2-fabric-mod-26.2.mrpack <instance>`
```

Every jar is identified by hash, so the pack references each mod by URL. A jar
Modrinth does not publish cannot be referenced that way, so its bytes are
embedded under `overrides/` with its real digests — the pack always round-trips.
`import` verifies every file against the manifest before it lands.

---

## Commands

| Command | What it does |
| --- | --- |
| `scan [instance]` | Identify every jar and record what it is. `--force` re-resolve, `--all` show unmatched rows inline |
| `list [instance]` (`ls`) | List installed jars with their resolved identity. `--known` hides unmatched, `--force` re-resolve |
| `outdated [instance]` (`check`) | Which mods have a newer build for *this* Minecraft version and loader |
| `watch [instance]` | Poll for updates on a timer and print only when the set changes. `--every`, `--json` |
| `update [instance]` (`up`) | Install available updates. Interactive by default; `--all`, `--dry-run`, `--yes`, `--include`, `--exclude`, `--allow-downgrade`, `--quiet-summary` |
| `migrate <source> <target>` | Copy mods between instances, resolving versions for the target. `--dry-run`, `--copy-unknown`, `--allow-downgrade`, `--with-configs`, `--version-channel`, `--include`, `--exclude`, `--yes`, `--quiet-summary` |
| `add <project...> [instance]` | Install from Modrinth by slug, project id or URL. `--channel`, `--no-deps`, `--dry-run`, `--yes` |
| `remove <mod...> [instance]` (`rm`) | Remove by name, mod id or file name. `--keep-files` moves to the backup dir instead of deleting, `--dry-run`, `--yes` |
| `info <project>` | Show a Modrinth project's details |
| `search <query>` | Search Modrinth. `-n/--limit`, `--loader`, `--category`, `-s/--sort` |
| `rollback [instance]` | Put back the jars the last `migrate` or `update` replaced. `--list` to see snapshots, `--to <stamp>` for a specific one, `--dry-run`, `--yes` |
| `export <dir> [instance]` | Write the instance's mods as a Modrinth `.mrpack`, pinned to the exact installed versions. `--name`, `--include-overrides` |
| `import <file.mrpack> [instance]` | Install a Modrinth `.mrpack`. `--no-overrides` for mods only, `--dry-run`, `--yes` |
| `link <mod> <project>` | Pin a jar to a project by hand (authoritative; never overwritten) |
| `unlink <mod>` (`unpin`) | Drop a manual link so automatic identification resumes |
| `doctor [instance]` | Diagnose the reasons Minecraft fails to start. `--fix` moves duplicate jars to the backup dir |
| `deps [instance]` | Declared dependencies and whether they are satisfied. `--missing` shows only the gaps |
| `cache` (`state`) | `info`, `path`, `clear`, `prune`, `forget <sha1>` |
| `instances` (`profiles`) | List the instances modharbor can see |
| `config` | `list`, `get <key>`, `set <key> <value>`, `path` |
| `completion <shell>` | Completion script for bash, zsh, fish or powershell |
| `version` | Version, build date and Go runtime |

### Global flags

| Flag | Meaning |
| --- | --- |
| `--config <path>` | Config file to use instead of the XDG default |
| `--minecraft <path>` | Minecraft directory, overriding auto-detection |
| `-i, --instance <ref>` | Default instance id or path for this run |
| `--json` | Machine-readable output; never prompts |
| `--no-color` / `--color` | Disable / force ANSI colour |
| `-q, --quiet` | Suppress decorative output |
| `-v, --verbose` | Verbose logging |
| `--offline` | Use cached data only; make no network requests |
| `--channel <name>` | `release`, `beta` or `alpha` for this run |
| `--loader <name>` | `fabric`, `forge`, `neoforge` or `quilt`; assumed when the instance declares none (e.g. a vanilla folder with no loader metadata) |
| `-h, --help`, `-V, --version` | Cobra's own |

---

## How identification works

`internal/resolver` tries four strategies in order and stops at the first one
that produces a *corroborated* match. The winner is recorded with its method and
a confidence score, which `scan` shows in the `MATCH` column.

| # | Method | How | Confidence |
| --- | --- | --- | --- |
| 1 | `hash` | SHA-1 of the jar → `GET /v2/version_file/{sha1}` | **1.00** (0.95 if the matched build targets a different Minecraft version) |
| 2 | `curseforge-id` | The CurseForge project id your launcher recorded in `TLauncherAdditional.json`, matched to a Modrinth project by name | **0.95** |
| 3 | `slug` | The jar's own mod id and display name, probed against Modrinth slugs (`yet-another-config-lib` from "Yet Another Config Lib") | **0.90** |
| 4 | `name` | Modrinth search, ranked by name similarity | **0.60 – 0.90** |

A `manual` link from `modharbor link` short-circuits everything and is
authoritative: automatic resolution never overwrites it.

Exact name equality scores 0.90, containment ("yacl" inside
"yetanotherconfiglibyacl") scores 0.80, and a 4-character-shingle Jaccard
similarity of 0.75 or better scores 0.60–0.80.

### Why 2–4 exist: the CurseForge mirror problem

This is the case that motivated the whole chain. Launchers routinely install
the **CurseForge mirror** of a mod. Those bytes are not the Modrinth copy, so:

1. `hash` misses. `/v2/version_file/{sha1}` returns 404 — not because the mod
   is unknown, but because the file is a different file.
2. `curseforge-id` saves it when the launcher wrote down a CurseForge project
   id. TLauncher's `TLauncherAdditional.json` records one per installed mod, and
   `instance.ParseTLauncherMods` reads it. MultiMC/Prism/ATLauncher sidecars are
   read too, and they carry the Minecraft version the instance actually runs.
3. `slug` and `name` cover launchers that keep no such record: the mod id
   inside `fabric.mod.json` is usually the Modrinth slug, and the display name
   usually is as well.

That is exactly the `Iris Shaders` row in the transcript above: repacked the way
a mirror repackages it, the SHA-1 lookup fails and the mod id `iris` finds it at
90% confidence.

### Fuzzy matches are corroborated, not trusted

Name agreement alone is not evidence. Two unrelated mods can share a name *and*
a version string — a private "More Tools" 1.0.0 jar is indistinguishable by name
from an unrelated published "More Tools (Polymer)" 1.0.0, and migrating on name
alone would install a stranger's mod. So before a `slug` or `name` match is
accepted, `resolver.versionCorroborated` requires:

- **A loader veto.** A jar whose metadata declares Fabric cannot be a
  Forge-only project, whatever the names look like. This is a hard constraint
  and it is free, because the project payload already lists its loaders.
- **Version agreement.** Modrinth version numbers are decorated
  (`mc26.2-0.9.1-fabric`, `0.154.2+26.2`, `fabric-26.3-2.3.8`), so the
  comparison uses the leading dotted-numeric core plus the normalised full
  string. If the project has *no* version with that core — normal for
  mirror-sourced jars where upstream renumbered — the match is allowed through,
  because there is nothing to compare.
- **File size agreement.** When a same-version build exists, its size must be
  within a factor of two. Repackaging changes a jar's bytes but not its size by
  an order of magnitude; two genuinely different mods that share a name and
  version usually differ substantially.

Anything that fails corroboration falls through to the next strategy, and if
all four fail the mod is reported as `unmatched` and **left alone**.

### The CurseForge provider is optional

CurseForge needs an API key for every request, so modharbor does not require
one:

```sh
modharbor config set curseForge.apiKey <your-key>
```

Without a key modharbor is fully functional via Modrinth. CurseForge-only mods
are simply reported as such (`published on CurseForge only; install it from the
CurseForge app or launcher`) rather than being probed with a numeric id that
Modrinth will never answer.

---

## Safety

**Every download is verified by SHA-512 before it becomes visible.** The bytes
stream into a temporary file next to the destination; only after the digest
matches is the file renamed into `mods/`. A failed or truncated download leaves
a `.modharbor-*` temp file that is deleted, never a broken jar where Minecraft
would try to load it.

**Nothing is replaced without a backup, and a backup is one command away.**
Files that a migration is about to replace are moved into a timestamped
snapshot first:

```
<mods>/.modharbor-backup/20261002T150405Z/
```

`modharbor rollback` puts them back. Restoring is itself undoable: whatever is
in `mods/` is swept into a fresh snapshot before anything is restored, so a
rollback can be rolled back.

```console
$ modharbor rollback --list
────────────────────────────────────────────────────────────────────────
  Snapshots
  26.3-fabric-mod (MC 26.3, Fabric)  ~/.minecraft/versions/26.3-fabric-mod/mods/.modharbor-backup
────────────────────────────────────────────────────────────────────────

     SNAPSHOT (UTC)    FILES  TAKEN
  ─────────────────────────────────────
  ★  20261002T142027Z      4  7.3s ago

✔ snapshot, 4.6 MB
  └─ restore the newest with `modharbor rollback 26.3-fabric-mod`
```

`doctor --fix` and `remove --keep-files` use the same directory under
`duplicates/` and `removed/`. Those two are *buckets*, not snapshots:
`rollback` deliberately skips them, because restoring them would undo a removal
you asked for.

**Duplicate jars are collapsed and reported, never silently dropped.** Two
copies of Fabric API in one folder is extremely common — a stale build plus a
fresh one. Both resolve to the same upstream project and would be written to the
same destination file name, so modharbor keeps the newest, reports the rest as
`duplicate`, and names the file that would have been overwritten. (The game
would silently load only one of them anyway.)

**Installs are ordered so a failure cannot lose a mod.** `install` and `copy`
actions run concurrently first; `replace` actions are applied afterwards, one
at a time. A mod is never removed before its replacement is safely on disk.

**`--dry-run` works on every mutating command** — `migrate`, `update`, `add`,
`remove`, `rollback` and `import` — and prints exactly the plan that a real run
would execute.

**Interactive by default.** `migrate` shows you the plan and asks
`Apply these changes? [y/N]` before writing; `update` lets you tick the mods you
want. `--yes` skips the prompts, `--json` disables them entirely.

---

## Configuration

Settings live at `$XDG_CONFIG_HOME/modharbor/config.json` (`~/.config/modharbor/config.json`
on Linux), written with mode `0600` because the file can hold API keys.
`config list` shows the effective settings.

```json
{
  "minecraftDir": "/home/you/.minecraft",
  "defaultInstance": "26.3-fabric-mod",
  "cacheDir": "/home/you/.cache/modharbor",
  "modrinth": {
    "enabled": true,
    "baseUrl": "https://api.modrinth.com/v2"
  },
  "curseForge": {
    "enabled": true,
    "baseUrl": "https://api.curseforge.com/v1",
    "apiKey": ""
  },
  "update": {
    "channel": "release",
    "includeSnapshots": false,
    "concurrency": 6,
    "hardDelete": false,
    "verifyDownloads": true,
    "autoBackup": true
  },
  "display": {
    "noColour": false,
    "forceColour": false,
    "json": false
  }
}
```

```console
$ modharbor config list
────────────────────────────────────────────────────────────────────────
  Configuration
  ~/.config/modharbor/config.json
────────────────────────────────────────────────────────────────────────

  minecraftDir       ~/.minecraft
  defaultInstance    26.3-fabric-mod
  channel            release
  concurrency        6
  modrinth.url       https://api.modrinth.com/v2
  modrinth.apiKey    not set
  curseForge.apiKey  not set
  verifyDownloads    true
  autoBackup         true

  └─ change one with `modharbor config set <key> <value>`
```

Settable keys: `minecraftDir`, `defaultInstance`, `channel`,
`concurrency` (1–16), `modrinth.apiKey`, `modrinth.baseUrl`,
`curseForge.apiKey`, `verifyDownloads`, `autoBackup`.

Resolutions and API responses are cached in a separate document at
`$XDG_DATA_HOME/modharbor/state.json`, keyed by SHA-1, which is why `list` and
`scan` are instant on the second run and why `--offline` is useful. Inspect it
with `modharbor cache info`.

Environment variables: `MODHARBOR_MINECRAFT_DIR` overrides the Minecraft
directory, `MODHARBOR_NONINTERACTIVE=1` disables prompts, and the usual
`NO_COLOR`, `CLICOLOR_FORCE` and `TERM` conventions are honoured.

---

## Troubleshooting

### "not published on Modrinth" / a mod that was not matched

Some mods are not on Modrinth at all, and some jars come from a pack that was
built by hand. Check what modharbor thinks:

```sh
modharbor scan --all                 # show unmatched rows inline
modharbor scan --json | jq '.mods[] | select(.confidence < 0.8)'
```

If the mod *is* published and modharbor just picked the wrong project, pin it:

```sh
modharbor link moretools-1.0.0.jar sodium
modharbor unlink moretools           # and this puts it back to automatic
```

If it is private, local, or built from your own source, carry it verbatim with
`--copy-unknown`:

```sh
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --copy-unknown
```

### "only a pre-release build exists for MC 26.3 — retry with --channel beta"

The author has published a build for your Minecraft version, but on the beta or
alpha channel. Widen the channel:

```sh
modharbor migrate 26.2-fabric-mod 26.3-fabric-mod --channel beta
modharbor outdated --channel alpha
```

This is a client-side filter: modharbor never sends `version_type` to Modrinth
(because the API accepts only one value, and filtering server-side would hide
exactly the pre-release builds you want to see about). Release, beta and alpha
are filtered and ranked locally.

### A CurseForge-only mod

```
⊘ Create [some mod]   published on CurseForge only; install it from the CurseForge app or launcher
```

Nothing is on Modrinth to migrate to. Install it in the target instance from
the CurseForge app or your launcher, and modharbor will leave it alone from then
on. A CurseForge API key does not change this — it only lets modharbor read
CurseForge metadata.

### "no fabric build for MC 26.3"

The mod has not shipped support for your new release yet. Nothing modharbor can
do; `doctor` will keep reporting it. Remove it from the new instance with
`modharbor remove betterf3` and wait for the author.

### "missing dependency"

```
✖  Lithium is missing dependency
    mixinextras >=0.5.5
    fix: install with `modharbor add <mod>`
```

Usually the mod really does need something you do not have; `modharbor add` will
pull it in. If it is a false positive, modharbor already counts the libraries
bundled *inside* other jars (Sodium ships several Fabric API modules under
`jars/`) and the ids the loader provides (`fabricloader`, `minecraft`, …). Use
`modharbor deps --missing` for the short list.

### An update broke the game

Every jar modharbor replaced is in a timestamped snapshot. Put them back:

```sh
modharbor rollback --list              # what can be restored
modharbor rollback --dry-run           # see the plan
modharbor rollback                     # restore the newest snapshot
modharbor rollback --to 20261002T142027Z
```

If `rollback` reports no snapshots, that instance has not had a `migrate` or
`update` that replaced anything — and if it mentions jars in `removed/`, those
came from `remove --keep-files`, which is a separate bucket and not a rollback
target. Move them back by hand if you want them.

### Minecraft still crashes

```sh
modharbor doctor
```

It checks for mods that do not support your Minecraft version, missing required
dependencies, two jars providing the same mod id (where one silently shadows
the other), and jars left over from a different Minecraft release. `--fix` moves
duplicate jars into the backup directory rather than deleting them.

### It is not seeing the right directory

```sh
modharbor instances        # what modharbor discovered
modharbor config list      # the effective minecraftDir
```

Set `modharbor config set minecraftDir <path>` if auto-detection guessed wrong.

### Everything is slow or offline

```sh
modharbor scan --offline        # resolve from the cache only
modharbor cache info            # is anything cached?
modharbor cache prune           # drop expired API responses
```

---

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for
the development setup, project layout, and conventions, and
[SECURITY.md](SECURITY.md) for reporting a vulnerability.

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 MohammadMD1383.