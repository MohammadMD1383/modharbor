# Providers

modharbor talks to two upstream APIs: **Modrinth**, which is required and
sufficient for everything modharbor does, and **CurseForge**, which is optional
because it needs an API key.

## Modrinth

`internal/provider/modrinth/modrinth.go`. Modrinth is the primary provider
because it is the only one of the two that offers exact hash lookup, which is
what turns identification from a guess into a lookup.

Base URL: `https://api.modrinth.com/v2` (overridable via
`modrinth.baseUrl`).

### Endpoints used

| Method | Path | Used by |
| --- | --- | --- |
| `GET` | `/version_file/{sha1}` | `VersionByHash` — the exact identification step |
| `GET` | `/version/{id}` | `VersionByID`, and the fallback in `VersionsBatch` |
| `POST` | `/versions` | `VersionsBatch` — up to 100 ids per request |
| `GET` | `/project/{id\|slug}` | `Project` — metadata, loaders, categories |
| `GET` | `/project/{id}/version` | `Versions` — version listing |
| `GET` | `/search` | `Search` — name-search identification, `modharbor search` |
| `GET` | `/data/{hash}` | `CDNURL` — resolve a mirror URL to a CDN URL |
| `POST` | `/versions` | `VersionsBatch` — used by `.mrpack` import to resolve many version ids in one request |

### Typed payloads

`Project`, `Version`, `File`, `Dependency`, `Hit` and `SearchResponse` mirror the
wire format exactly. Two helpers carry most of the policy:

```go
func (v Version) PrimaryFile() (File, bool)   // primary .jar, else first .jar
func (v Version) Supports(mcVersion, loader string) bool
```

`File.SHA1()` and `File.SHA512()` read out of `File.Hashes`, so a caller never
hard-codes a digest algorithm.

`CDNURL` is worth explaining. Modrinth hands back a mirror URL of the form
`https://api.modrinth.com/v2/data/<hash>`. `CDNURL` recognises that shape,
asks the API for the real `cdn.modrinth.com` URL, and falls back to the original
when the path is not recognised (a third-party mirror, say) or the lookup fails.
CDN downloads are faster and more reliable when pulling dozens of jars.
`mrpack.entryFor` uses it too, and deliberately prefers the CDN URL over the
mirror for the same reason: it survives a project changing mirrors.

### Channel filtering is client-side

`Client.Versions` deliberately does **not** send `version_type`:

```go
// version_type is deliberately NOT sent upstream. Modrinth accepts only a
// single value there, and filtering server-side would hide pre-release
// builds that do support the target game version.
```

Because the parameter accepts one value at a time, `?version_type=beta` would
also drop every release. Worse, filtering server-side would hide exactly the
information a user needs: "a beta exists for your game version, but you are on
the release channel".

Instead:

```go
modrinth.FilterByChannel(vers, ch)   // filter + sort newest-first
modrinth.TypeRank(versionType, ch)   // ordering within the channel
modrinth.AllowsChannel(versionType, ch)
```

| Channel | Accepted `version_type` values | Ordering |
| --- | --- | --- |
| `release` | `release` | newest first |
| `beta` | `release`, `beta` | newest first, release wins ties |
| `alpha` | `release`, `beta`, `alpha` | newest first, release then beta wins ties |

Because filtering happens locally, `migrate.Engine.excludeReason` can look at
the versions that *were* dropped and say precisely
`only a pre-release build exists for MC 26.3 — retry with --channel beta`.

### Requests, retries and caching

All requests funnel through one private method:

```go
func (c *Client) do(ctx, method, path string, body any, query url.Values, out any) error
```

- **User-Agent.** `modharbor/1.0 (https://github.com/MohammadMD1383/modharbor)`.
  Modrinth requires a descriptive agent and blocks generic ones.
- **404 → `ErrNotFound`** immediately, wrapped with the path. This is a normal
  outcome, not an error to retry: `resolver.byHash` uses it as a signal to move
  on to the next strategy, and `migrate.isMissingProject` turns it into
  "not published on Modrinth".
- **429 → honour `Retry-After`** (seconds or HTTP-date, via `retryAfter`) and
  retry, up to four attempts total.
- **5xx → retry** with exponential backoff: 500 ms, 1 s, 2 s.
- **Other 4xx → typed `*APIError`**, returned immediately. Retrying a bad
  request is pointless.
- **Transport errors → retried** on the same backoff schedule.
- **Context cancellation** is honoured during backoff, so Ctrl-C is immediate.

Rate limiting is handled by being a good client rather than by throttling:
`Client.throttle` exists (a `minGap` floor between calls) but is configured to
zero, because Modrinth allows generous rates for well-behaved clients. The real
concurrency control is in `resolver.ResolveAll`, which fans out with a
**6-goroutine semaphore** across the mods folder. That number is chosen to keep
a 200-jar pack comfortably inside the API's limits while still being fast
enough that identification is not the bottleneck.

Responses are cached in memory for the lifetime of the process:

- `cacheGet` / `cacheSet` keyed by operation and argument
  (`vh:<sha1>`, `proj:<idOrSlug>`, `vid:<id>`)
- TTL from `Options.CacheTTL`, set to 10 minutes by `app.New`
- `Project` and `VersionByHash` cache both hits and decoded values, returning
  the cached pointer

This matters more than it looks. Resolving one mods folder asks about the same
project several times: once for identification, once for corroboration, once for
version selection.

`Resolver` adds a second, coarser layer: `versionsCached` memoises full version
listings per project id for the lifetime of the resolver, shared across every
`ForInstance` scope through a shared `*sync.Mutex`.

### Tests

`internal/provider/modrinth/modrinth_test.go` runs entirely against
`httptest.Server`, covering hash lookup, 404 handling, game-version and loader
filtering, channel filtering and ranking, batching, facet construction, CDN URL
resolution and its fallback, User-Agent, the in-memory cache, and retry on
5xx. No test touches the real API.

## CurseForge

`internal/provider/curseforge/curseforge.go`. Optional, and honestly optional:
modharbor is fully functional without it.

Base URL: `https://api.curseforge.com/v1`.

### What it needs

CurseForge requires an `x-api-key` header on every request, so:

```go
func (c *Client) do(...) error {
    if !c.HasKey() { return ErrNoAPIKey }
    ...
}
```

The client refuses to make requests rather than firing them and collecting
confusing 403s. Set a key with:

```sh
modharbor config set curseForge.apiKey <your-key>
```

`app.App.CF()` constructs it lazily behind a `sync.Once`. `401` and `403` are
translated into an `*Error` with the message "invalid or missing API key".

### What it is used for

Two things, neither of which is downloading:

1. **Launcher metadata, not the API.** The valuable integration is reading
   `TLauncherAdditional.json` (`instance.ParseTLauncherMods`), which records a
   CurseForge project id for every installed mod. That is the input to
   `resolver.byCurseForgeID` — strategy 2 — and it needs no API key at all.
   `curseforge-id` matches the launcher's display name against Modrinth
   projects; a hit yields a Modrinth identity at 0.95 confidence.

   When no Modrinth equivalent exists, the resolution is recorded with
   `Provider: "curseforge"` and the numeric id, and `migrate.Engine.decide`
   reports `published on CurseForge only; install it from the CurseForge app or
   launcher`. It deliberately does **not** probe Modrinth with that numeric id:
   CurseForge and Modrinth id spaces are unrelated, and trying would produce a
   confident wrong answer.

2. **Direct API reads** where a key is present: `Mod`, `Files`, `ModFile`,
   `Search`, `GameVersionID` (Minecraft version string → numeric id),
   `LoaderID` (loader name → category id), and `DownloadBody` for proxied
   downloads.

### Shape normalisation

CurseForge returns numeric fingerprints and integer ids; Modrinth returns hex
digests and slugs. `hydrateFingerprints` copies the numeric fingerprint list into
the `SHA1` / `SHA512` string fields so both providers expose the same shape:

```go
type ModFile struct {
    ...
    SHA1   string `json:"-"`
    SHA512 string `json:"-"`
}
```

`FingerprintSHA1 = 1`, `FingerprintMD5 = 2`, `FingerprintSHA512 = 4`.
`GameVersions` falls back to `SortableGameVersions` when empty, since the
sortable list is the more reliable one.

Loader names are uppercased (`FABRIC`, `FORGE`, `NEOFORGE`, `QUILT`) and mapped
to known category ids (Fabric 4, Forge 1, NeoForge 6, Quilt 5) so `LoaderID`
needs no request in the common case.

### Rate limiting

Unlike Modrinth, the CurseForge client has **no retry loop**. `do` issues one
request and maps the status. This is a real asymmetry and worth knowing about:
if CurseForge support is extended, `do` should grow the same
retry-with-`Retry-After` behaviour Modrinth's `do` has.

## Adding a third provider

There is deliberately **no provider interface today**. With two providers of
this shape, an abstraction would be speculative; when the third arrives, shape
it then. That said, the integration points are few and well-defined.

### 1. Write the client

Create `internal/provider/<name>/<name>.go`. Follow
`internal/provider/modrinth/modrinth.go`:

- `DefaultBaseURL` and `UserAgent` constants. The User-Agent must name the
  project and its URL; both existing providers reject generic agents.
- A `Client` struct: `baseURL`, `*http.Client`, `userAgent`, a mutex-guarded
  response cache, and any credentials.
- `New(Options) *Client`, defaulting `BaseURL` and `UserAgent`, and building an
  `*http.Client` with a sensible timeout and a keep-alive transport.
- One private `do(ctx, method, path, body, query, out)` that owns **all**
  transport concerns, in this order:
  1. credential check (if the provider needs one),
  2. `User-Agent` and `Accept` headers,
  3. `404 → ErrNotFound`, returned immediately,
  4. `429 → honour Retry-After, retry`,
  5. `5xx → retry with exponential backoff`,
  6. other `4xx → typed *APIError`, returned immediately`,
  7. decode into `out`.

  Four attempts maximum. Honour `ctx.Done()` during backoff. Never let a
  transport error escape unretried.
- Typed payloads with `json` tags matching the wire format, plus the small
  helper methods that carry policy: a `PrimaryFile()` equivalent, a
  `Supports(mcVersion, loader)` equivalent, digest accessors.
- Cache every `GET`. See [the Modrinth caching section](#requests-retries-and-caching)
  for why this matters more than it looks.
- Keep a concurrency ceiling on batch operations. `resolver.ResolveAll`'s
  semaphore is 6; a provider that fans out internally should stay inside that
  or the effective rate doubles.

### 2. Make it optional if it needs credentials

Mirror CurseForge: a `HasKey()` predicate, an `ErrNoAPIKey` sentinel whose
message tells the user the exact `config set` command to run, and a lazily
constructed client on `app.App` behind a `sync.Once`. The tool must remain fully
functional when the provider is not configured — that is the contract, not a
polish item.

### 3. Add the config stanza

```go
type Config struct {
    ...
    YourProvider ProviderConfig `json:"yourProvider"`
}
```

`ProviderConfig` already has `enabled`, `apiKey` and `baseUrl`. Then:

- `config.Default()` — set `BaseURL` and `Enabled`,
- `Config.Merge` — fill the `BaseURL` when empty,
- `config list` / `configLookup` / `configSet` in `internal/cli/cache.go` — add
  the rows so `config list` reports its state,
- nothing else: `config.Save` already writes the file `0600`, which is what
  makes it safe to hold a key here.

### 4. Add an identification strategy

In `internal/resolver/resolve.go`:

- Add a `Method<Name>` constant to the method block, with a doc comment saying
  what it means.
- Add a `conf<Name>` floor to the confidence block. Set it honestly: it should
  reflect how much evidence the strategy actually used, because `scan` shows it
  to the user as a percentage and the user makes trust decisions from it.
- Write `Resolver.by<Name>(ctx, Candidate) *store.Resolution`, returning `nil`
  when the strategy does not fire. It must apply the same corroboration
  discipline: return `nil` rather than a match you cannot defend.
- Insert it into `Resolve` at the correct point in the chain — the order is
  strongest-evidence-first, and `Resolve` returns the first non-nil result.

Extend the memoisation if your provider needs it. `Resolver.versions` is
Modrinth-specific (`map[string][]modrinth.Version`); if the second provider
becomes a real source of version data, widen that to a small per-provider cache
behind the same shared `versionsMu`.

### 5. Be honest about id spaces

Provider-native ids are not interchangeable. Set `Resolution.Provider`
correctly so `migrate.Engine.decide` can say "published on X only" instead of
probing a foreign id space and reporting a confident wrong answer.

### 6. Surface it to the user

- `cli/scan.go`'s `methodLabel` — add the method so the `SOURCE` column shows
  it.
- `cli/add.go`, `cli/search.go`, `cli/info.go` — extend the search and
  install paths if the provider is searchable.
- `README.md`'s "How identification works" table — add the row with its
  confidence.
- `CHANGELOG.md` under `## [Unreleased]`.

### 7. Test it

Use `httptest.Server` and never touch a real API in tests. Cover: the happy
path, `404 → ErrNotFound`, retry on `5xx`, `Retry-After` on `429`, credential
absence returning the sentinel without a request, User-Agent presence, and the
response cache. `internal/provider/modrinth/modrinth_test.go` is the model.

### Checklist

- [ ] `New(Options)` with defaults for `BaseURL` and `UserAgent`
- [ ] One `do` owning retries, `Retry-After`, `ErrNotFound`, typed errors
- [ ] `ctx` honoured during backoff; four attempts maximum
- [ ] `ErrNoAPIKey` instead of an unauthorised request, if credentials are needed
- [ ] Every `GET` cached, with a TTL
- [ ] Typed payloads plus `PrimaryFile` / `Supports` / digest accessors
- [ ] Config stanza: `Default`, `Merge`, `config list`, `get`, `set`
- [ ] `Method` constant and honest `conf` floor
- [ ] Corroboration applied before any fuzzy match is accepted
- [ ] `Resolution.Provider` set correctly for provider-only projects
- [ ] `methodLabel` updated so `scan` shows the method
- [ ] Tests against `httptest.Server`, no live API
- [ ] `README.md` and `CHANGELOG.md` updated