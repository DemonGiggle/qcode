# qcode User Manual (PDF)

Usage-focused guide for people using qcode. It intentionally omits
implementation and architecture details and shows how to use each feature.

- **PDF:** [`qcode-user-manual.pdf`](qcode-user-manual.pdf)
- **Screenshots:** [`assets/`](assets/) — illustrative terminal screens
  rendered for print clarity (TUI layout, `/help`, `/model`, agents,
  Plan mode, `/remote` QR, diff/export).

## Regenerate

Requires Python 3 with `fpdf2` and `Pillow` (both pure-Python wheels):

```sh
pip install fpdf2 Pillow
make manual
# or:
python3 docs/manual/generate_manual.py
```

Output is `docs/manual/qcode-user-manual.pdf` plus refreshed PNGs in
`docs/manual/assets/`. Commit both the script output and sources so the
manual is reviewable as a PDF in the PR.

## Scope rule

Keep this manual about **using** qcode: install, prompt, queue, commands,
models, agents, plans, skills, memory, web tools, sessions, browser control,
settings, and troubleshooting. Do not add package layouts, control-plane,
event-bus, provider wire formats, or other internals here.
