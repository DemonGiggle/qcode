# Building

Go 1.22 or newer is required.

```sh
make build
./bin/qcode
```

## Versioning

When the current commit has a Git tag, builds use that tag for `qcode --version`. Untagged builds report `dev`. Set `VERSION=...` explicitly to override automatic detection.

## Release builds

Release builds set `CGO_ENABLED=0`, so the binary has no C runtime or third-party dynamic-library dependency. On Linux, `ldd bin/qcode` should report `not a dynamic executable`. An operating system may still load its own core system components, particularly on Windows; “self-contained” means no separately installed qcode runtime or third-party shared library.

Build all supported targets from any host with Go installed:

```sh
make release VERSION=0.1.0
```

This creates Linux (`amd64`, `arm64`), macOS (`amd64`, `arm64`), Windows (`amd64`, `arm64`), and FreeBSD (`amd64`) executables in `dist/`.
