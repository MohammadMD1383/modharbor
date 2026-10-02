# Security Policy

## Supported versions

| Version | Supported |
| --- | --- |
| 0.1.x | ✅ |

modharbor is a single-user CLI. Fixes land on `main` and are released promptly.

## Reporting a vulnerability

**Please do not open a public issue.**

Use GitHub's private reporting flow: go to
[**Security → Report a vulnerability**](https://github.com/MohammadMD1383/modharbor/security/advisories/new)
on the repository, or use the **Report a vulnerability** button under
**Security**. That opens a private advisory visible only to you and the
maintainers, so a fix can be prepared before anything is public.

Please include:

- the modharbor version (`modharbor version`),
- your OS and architecture,
- the command you ran and the full output, with any paths or API keys
  redacted,
- what an attacker gains, and what you had to do to trigger it.

You can expect an acknowledgement within a few days, and a fix or a mitigation
plan once the report is confirmed. Please give the maintainers reasonable time
to release a fix before disclosing publicly.

## What counts as a vulnerability

modharbor writes to your filesystem and downloads files from the internet, so
the interesting surface is:

- **Arbitrary file write or deletion** outside the instance's `mods/`
  directory, or via a crafted jar or metadata descriptor.
- **Checksum bypass** — a way to get a jar into `mods/` whose SHA-512 does not
  match what the provider published. Downloads are streamed to a temporary file
  in the destination directory and renamed into place only after the digest
  matches; a bug in that path is a reportable finding.
- **Path traversal** through a jar's name, a Modrinth `filename`, a launcher
  sidecar, or an `.mrpack` manifest entry. The temp files modharbor writes
  (`.modharbor-dl-*`, `.modharbor-cp-*`, `.modharbor-mrpack-*`,
  `.modharbor-*`) must not be usable as an escape hatch.
- **Zip bombs or unbounded reads.** Metadata descriptor reads are capped at
  4 MiB per entry and nested jars at 64 MiB; a way around those caps matters.
- **Command injection** — modharbor does not shell out to anything, but a
  regression that introduces process execution is reportable.
- **Secret disclosure** — leaking or over-reading the config file, which can
  contain API keys.

Ordinary bugs are not vulnerabilities: a mod matched to the wrong project (fixed
with `modharbor link`), a corrupt cache file, an instance whose Minecraft version
cannot be detected.

## Credentials

- **Config file permissions.** `config.Save` writes
  `$XDG_CONFIG_HOME/modharbor/config.json` with mode **`0600`**, because the
  document can hold a CurseForge or Modrinth API key. Do not relax this, and
  please report it if the file is ever created world-readable.
- **What modharbor stores.** Only what you put there via
  `modharbor config set`. The state document at
  `$XDG_DATA_HOME/modharbor/state.json` holds jar SHA-1 digests, resolved
  project ids and instance paths — no credentials.
- **What leaves your machine.** Requests to the Modrinth API (and to CurseForge
  if a key is configured) carry a User-Agent of
  `modharbor/1.0 (https://github.com/MohammadMD1383/modharbor)` and, for
  CurseForge, the `x-api-key` header. Nothing else is transmitted, and
  `--offline` makes no network requests at all.
- **Backups.** Replaced jars are moved to `<mods>/.modharbor-backup/<timestamp>/`
  rather than deleted, and `modharbor rollback` puts them back. Removing your
  `mods/` directory without also removing that subdirectory leaves the old jars
  on disk — including, in `removed/` and `duplicates/`, jars you deleted or had
  moved aside. Those two buckets are never rollback targets, by design.
- **Path traversal in packs.** `import` reads file paths out of an untrusted
  `.mrpack`. Overrides are written beneath the instance root; a manifest path
  that escapes it is a reportable finding.

## Hardening your own setup

- A CurseForge key is optional. Without one, modharbor is fully functional via
  Modrinth, so only set a key if you want CurseForge-only projects reported
  precisely. See [README](../README.md#the-curseforge-provider-is-optional).
- Prefer `--dry-run` first on a new Minecraft release, and read the plan.
- `--dry-run` performs no writes, so it is safe to run against a production
  instance.