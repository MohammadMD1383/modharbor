---
name: Bug report
about: Something in modharbor does not work as documented
title: ''
labels: bug
assignees: ''
---

## What happened

<!-- A short description of the wrong behaviour. -->

## What you expected

<!-- What modharbor should have done instead. -->

## Steps to reproduce

1.
2.
3.

## Diagnostics

Please paste all three of these. They contain the information needed to work out
what went wrong, and you can redact instance paths if you prefer.

```
$ modharbor version
```

```
$ modharbor instances
```

```
$ modharbor doctor --json
```

## Your setup

- **Minecraft version:**
- **Loader:** <!-- fabric / forge / neoforge / quilt -->
- **OS:** <!-- e.g. Fedora 41, Windows 11, macOS 15 -->
- **modharbor version:** <!-- from `modharbor version` above -->
- **Install method:** <!-- release binary, `go install`, package manager, source build -->

## Anything else

<!-- Full command output with `modharbor -v` if it is relevant, plus any mod
     list output (`modharbor list`) that shows the affected mods. -->

<!--
Please do not attach whole instance directories or config files that contain
tokens or private URLs. Logs and JSON output are enough.
-->