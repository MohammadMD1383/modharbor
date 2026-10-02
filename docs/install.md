# Installing modharbor

Three ways to get it. All three come out of the same tagged release, so pick
whichever fits how you already work.

## 1. `go install`

```sh
go install github.com/MohammadMD1383/modharbor/cmd/modharbor@latest
```

Needs Go 1.23 or newer. This is the only method that needs nothing but a Go
toolchain — but it only works **once a `v*` tag exists**, because `@latest`
resolves against the module proxy's list of tagged versions. Before v0.1.0 there
is nothing to resolve, and the command fails with an "unknown revision"
message rather than installing anything.

If you install this way, that is the only install path you need: `GOBIN`
(or `GOPATH/bin`, or `~/go/bin`) has to be on your `PATH`.

## 2. Prebuilt binaries

Every tagged release attaches one archive per platform to
[GitHub Releases](https://github.com/MohammadMD1383/modharbor/releases). The
binaries are static (`CGO_ENABLED=0`), so they run on any distro without a
matching libc.

| Platform | Asset | Contents |
| --- | --- | --- |
| linux/amd64 | `modharbor_<version>_linux_amd64.tar.gz` | `modharbor`, `LICENSE`, `README.md`, `CHANGELOG.md` |
| linux/arm64 | `modharbor_<version>_linux_arm64.tar.gz` | same |
| darwin/amd64 | `modharbor_<version>_darwin_amd64.tar.gz` | same |
| darwin/arm64 | `modharbor_<version>_darwin_arm64.tar.gz` | same |
| windows/amd64 | `modharbor_<version>_windows_amd64.zip` | `modharbor.exe`, `LICENSE`, `README.md`, `CHANGELOG.md` |
| windows/arm64 | `modharbor_<version>_windows_arm64.zip` | same |

**Windows is `.zip`, not `.tar.gz`.** The archives for every other platform are
`.tar.gz`; Windows gets `.zip` because that is what Windows can open without a
third-party tool. Nothing else differs between the platforms.

Plus two linux packages per architecture:

| Platform | Assets |
| --- | --- |
| linux/amd64 | `modharbor_<version>_linux_amd64.deb`, `modharbor_<version>_linux_amd64.rpm` |
| linux/arm64 | `modharbor_<version>_linux_arm64.deb`, `modharbor_<version>_linux_arm64.rpm` |

They install `modharbor` into `/usr/bin/modharbor` and the `LICENSE`,
`README.md` and `CHANGELOG.md` into `/usr/share/doc/modharbor/`.

There is no AUR, Homebrew, Scoop or Chocolatey package. The release workflow
builds archives and deb/rpm only — see `.goreleaser.yaml` — so do not expect a
`brew install modharbor` or an AUR package to work. `go install` and the
archives above are the whole list.

## 3. Verifying what you downloaded

Every release attaches a `checksums.txt` covering all ten files above. Check it
before you run anything:

```console
$ sha256sum -c checksums.txt
modharbor_0.1.0_linux_amd64.tar.gz: OK
…
```

Then confirm the binary is the one you think it is:

```console
$ modharbor version
modharbor 0.1.0 (abc1234)
  built:    2026-10-02T15:04:05Z
  runtime:  go1.23.0 linux/amd64
```

The `built:` line is the commit time stamped in at link time
(`.goreleaser.yaml` sets it via `-ldflags -X`), so it changes with every tag. The
`runtime:` line is the Go release the binary was *built* with, not the one you
run it with — it will not change if you copy the binary to a newer machine.

## Building it yourself

```sh
git clone https://github.com/MohammadMD1383/modharbor
cd modharbor
make build          # bin/modharbor, version/commit/date stamped in
make snapshot       # goreleaser release --snapshot --clean, no publishing
```

`make snapshot` is the honest way to prove a change to `.goreleaser.yaml` still
works. It writes to `dist/`, which is gitignored, and uploads nothing.