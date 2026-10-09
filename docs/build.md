# Building

Go 1.22 or newer is required.

```sh
make build
./bin/qcode
```

`make build` writes `bin/qcode`; it does not install the executable on `PATH`.
Use `./bin/qcode` from the checkout or install the binary in a directory on
your shell's `PATH`.

## Checks and documentation artifacts

```sh
make test
cd tools/qcode-tester && go test ./...
```

`make test` runs the root Go module's tests. The evaluator has a separate Go
module; its tests do not launch a real provider. `make eval` builds qcode,
runs those tests, and performs live evaluations using your current provider
configuration. See [qcode-tester](../tools/qcode-tester/README.md).

`make manual` regenerates the user manual PDF and illustrative PNG screens;
see the [manual build guide](manual/README.md) for Python dependencies and
visual checks. `make demo-record` refreshes the offline terminal recording,
and `make demo-gif` converts it with `agg`.

## Versioning

When the current commit has a Git tag, builds use that tag for `qcode --version`. Untagged builds report `dev`. Set `VERSION=...` explicitly to override automatic detection.

Installed release binaries can update themselves from the latest GitHub release:

```sh
qcode update
qcode update --arch arm64
```

The updater compares the installed version with the latest release, selects the
`qcode_<os>_<arch>` asset for the current operating system, downloads it beside
the executable, and atomically replaces the executable. On Windows, replacement
is completed by a short-lived helper after qcode exits.

## Release builds

Release builds set `CGO_ENABLED=0`, so the binary has no C runtime or third-party dynamic-library dependency. On Linux, `ldd bin/qcode` should report `not a dynamic executable`. An operating system may still load its own core system components, particularly on Windows; “self-contained” means no separately installed qcode runtime or third-party shared library.

Build all supported targets from any host with Go installed:

```sh
make release VERSION=0.1.0
```

This creates Linux (`amd64`, `arm64`), macOS (`amd64`, `arm64`), Windows (`amd64`, `arm64`), and FreeBSD (`amd64`) executables in `dist/`.
