# qcode-tester

`qcode-tester` is a black-box evaluator for qcode. It does not import qcode
packages. It launches a supplied qcode executable with only the scenario prompt
and evaluates its exit status, stdout, and workspace artifacts.

Run it from this directory after building qcode at the repository root:

```sh
go run ./cmd/qcode-tester --qcode-bin ../../bin/qcode
```

Use `--scenario NAME` to select a scenario, `--json` for a machine-readable
aggregate report, and `--keep-artifacts` to retain successful workspaces.
Failures are retained under `artifacts/` with stdout, stderr, the report, a
workspace manifest, and a workspace copy.

## Scenario format

Every directory below `testdata/scenarios/` contains a strict `scenario.json`.
Version 1 scenarios define a prompt, timeout, optional seed files, and
assertions. `files` requires an exact file match; `file_contains` checks that a
file contains a required string.

The target qcode process performs its normal configuration lookup. Configure
the provider, credentials, and model exactly as you would for ordinary qcode
use (including a `config.toml` next to the target executable when applicable).
qcode-tester does not create configuration, start a provider, or pass qcode
provider/model/base-URL/workspace/event flags. These are live evaluations, so
they are opt-in and are not run in CI.
