# AGENTS.md — rules for agents working in this repo

Read this before picking up work. `QUEUE.md` is the work queue, `HANDOFF.md` is
the long-form orientation. This file is the short list of rules that are not
negotiable, and it is loaded automatically — you do not need to be told.

## The standing rule: leave the tree documented

**Anything open, unaddressed, deferred, out of scope, or newly discovered gets
written down before you finish. Nothing gets dropped silently.**

This applies even when the work "succeeded". Concretely, before your last
commit, re-read what you did and account for every loose end:

- A bug you found but did not fix → a `QUEUE.md` item, with the evidence.
- A bug you fixed whose *cause* is broader than the fix → say so, and record
  the remainder.
- A question you could not answer → the question, written down as a question.
- Work you deliberately skipped → one line saying you skipped it and why.
- A pre-existing failure you ran into → recorded, even if unrelated to your
  change and even if you are sure someone else already knows.

An undocumented loose end is indistinguishable from a lost one. The next
person — or the next session, or you in a month — will not remember it, and it
will not be in git history because you never wrote it down. Assume the reader
is cold and has no access to your session.

**Where it goes.** New work goes in `QUEUE.md`, which is the cold-pickable
inventory: every item self-contained, so someone who has read nothing can pick
it up. If an item is a refinement of a tracked defect, cross-reference its
`BACKLOG.md` number. Record the *evidence* — the failing command, the log line,
the count — not just the conclusion, because the evidence is what lets the next
person trust it without redoing your work.

**When you find something while verifying**, before you go looking for the next
thing to fix. Discovering a defect is not a reason to abandon the task you were
given; it is a reason to write the defect down so it is not lost when you move
on. One item per unit of work still holds — documenting a finding is not
fixing it, so it does not violate scope.

## Verify before you commit

```sh
gofmt -l internal/ cmd/     # must print nothing
go build ./...
go vet ./...
go test ./...
go test -race ./...
make check
```

All clean. If you fixed a bug, **prove it was a bug before fixing it** and
**prove your test catches it after** — revert the fix, watch it go red, and put
that evidence in the commit message. A test that only characterises existing
code has no red phase; break the expected value and confirm a named test
notices.

Do not trust a subagent's report at face value. `HANDOFF.md` records two agents
that were cut off mid-task and left work which built and passed tests while
being wrong. Reproduce the claims yourself.

## The real Minecraft instances are OFF LIMITS

`~/.minecraft/versions/{26.2,26.3}-fabric-mod` — do not read, list, `cd` into,
scan, or run modharbor against them. **Do not `curl -O`**; it saves the URL's
escaped basename, which is how two stray `%2B`-encoded jars once landed in the
live `26.3/mods/` and crashed Minecraft. Use `curl -o "$SCRATCH/…"`.

Prove isolation with `unshare -rm` plus bind-masking, never by inspecting the
real paths. Everything else happens under a `mktemp -d` scratch dir. Tests use
`t.TempDir()` + `t.Setenv` for `MODHARBOR_MINECRAFT_DIR` and the `XDG_*` vars,
and `httptest.NewServer` for the API — **never live Modrinth or CurseForge.**

## Conventions

- One unit of work per branch, named for the *change* (not the first commit).
  Branch from current `main`.
- No new dependencies without discussion. The only dependency is cobra.
- Render through `internal/ui`, never bare `fmt.Println` of styled text.
- Every command respects `--json`, `--no-color`, `NO_COLOR`, and non-TTY stdout.
- `internal/resolver`'s `versionCorroborated` encodes a real incident: a private
  mod nearly matched a stranger's published mod. `collide_test.go` is its
  specification — those four tests must keep passing.
- Before claiming a PR is green: **CI on `main` has never been green.** See
  `QUEUE.md` section A. A red run tells you nothing about your change.