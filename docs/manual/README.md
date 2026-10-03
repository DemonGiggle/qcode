# qcode User Manual (PDF)

Usage-focused guide for people using qcode. It intentionally omits
implementation and architecture details and shows how to use each feature.

The **Use cases** pages after chapter 3 walk through repository onboarding,
bug fixes with queued checks, planning, independent agent tabs, reusable skills,
and resuming or sharing work. The guide also covers pinned latest prompts,
steering and pending-work removal, local redaction, terminal themes, transcript
boundary navigation, token totals, and workspace path shortening.

- **PDF:** [`qcode-user-manual.pdf`](qcode-user-manual.pdf)
- **Screenshots:** [`assets/`](assets/) — illustrative terminal screens
  rendered for print clarity (TUI layout, `/help`, `/model` names only,
  agents, Plan mode, `/remote` QR, diff/export, `/interactive` question).

## Regenerate

Requires Python 3 with `fpdf2`, `Pillow`, and DejaVu fonts (the generator uses
`/usr/share/fonts/truetype/dejavu/`):

```sh
pip install fpdf2 Pillow
make manual
# or:
python3 docs/manual/generate_manual.py
```

Output is `docs/manual/qcode-user-manual.pdf` plus refreshed PNGs in
`docs/manual/assets/`. Commit both the script output and sources so the
manual is reviewable as a PDF in the PR.

After regenerating, render the PDF pages and inspect them for clipping,
overlapping text, and screenshot legibility. For example, with Poppler:

```sh
pdftoppm -scale-to 1400 -png docs/manual/qcode-user-manual.pdf /tmp/qcode-manual
```

## Refresh the README demo

Requires Python 3 on Linux/macOS, Go, and [agg](https://github.com/asciinema/agg):

```sh
make demo-record
make demo-gif
```

The recording uses the current binary in `--demo` mode with a temporary home
and workspace. It shows the pinned latest prompt, Enter steering, Tab queueing,
the pending panel, mocked diffs, theme previews, transcript navigation, and model
search without calling a model or running real tools. Check representative GIF
frames against the recording after each refresh.

## Scope rule

Keep this manual about **using** qcode: install, prompt, queue, commands,
models, agents, plans, skills, memory, web tools, sessions, privacy, browser control,
settings, and troubleshooting. Do not add package layouts, control-plane,
event-bus, provider wire formats, or other internals here.

While an agent is busy, Enter steers its current task; Tab queues a separate task. In the browser, use Steer/Queue; Tab navigates normally. Steering waits for the current response or executing tool, withdraws unanswered interactions, and replaces earlier pending steering. Alt+Q opens pending work; Up/Down selects, Delete removes, and Esc closes. Ctrl+C cancels active work.
