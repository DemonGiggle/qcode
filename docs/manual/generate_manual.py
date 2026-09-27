#!/usr/bin/env python3
"""Generate the qcode User Manual PDF (usage-focused, no internals).

Usage:
    python3 docs/manual/generate_manual.py
    make manual

Output:
    docs/manual/qcode-user-manual.pdf
    docs/manual/assets/*.png  (illustrative terminal screenshots)

The screenshots are illustrative mock-ups rendered with Pillow so the manual
builds reproducibly without a running terminal. They show what users see when
they run each command, not how qcode is implemented.
"""
from __future__ import annotations

import datetime
from pathlib import Path
import random

from PIL import Image, ImageDraw, ImageFont

try:
    from fpdf import FPDF
except ImportError as exc:  # pragma: no cover
    raise SystemExit("fpdf2 is required: pip install fpdf2") from exc

ROOT = Path(__file__).resolve().parent
ASSETS = ROOT / "assets"
PDF_PATH = ROOT / "qcode-user-manual.pdf"

VERSION = "1.0"
DATE = datetime.date.today().strftime("%B %d, %Y")

# ---------------------------------------------------------------------------
# Screenshot rendering (Pillow)
# ---------------------------------------------------------------------------

BG = (11, 14, 20)
PANEL = (21, 27, 38)
FG = (230, 230, 230)
DIM = (138, 138, 150)
GREEN = (126, 231, 135)
CYAN = (121, 192, 255)
YELLOW = (227, 179, 65)
MAGENTA = (224, 156, 255)
RED = (255, 123, 114)
WHITE = (255, 255, 255)
SELECT_BG = (30, 60, 90)
STATUS_BG = (28, 33, 45)

MONO_CANDIDATES = [
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf",
    "/usr/share/fonts/truetype/noto/NotoSansMono-Regular.ttf",
]


def mono_font(size: int, bold: bool = False):
    for p in MONO_CANDIDATES:
        if "Bold" in p and not bold:
            continue
        if "Bold" not in p and bold:
            # Prefer bold file for bold weight when available.
            continue
        try:
            return ImageFont.truetype(p, size)
        except OSError:
            continue
    # Fall back to any mono that exists.
    for p in MONO_CANDIDATES:
        try:
            return ImageFont.truetype(p, size)
        except OSError:
            continue
    return ImageFont.load_default()


def draw_terminal(path: Path, title: str, lines: list[tuple[str, tuple, tuple | None, bool]], width: int = 1200):
    """Render a terminal-style PNG.

    lines: list of (text, fg_color, bg_color_or_None, bold)
    """
    font = mono_font(20, bold=False)
    font_bold = mono_font(20, bold=True)
    # Measure.
    ascent, descent = font.getmetrics()
    line_h = ascent + descent + 8
    pad_x, pad_top, pad_bottom = 28, 64, 24
    # Title bar.
    title_font = mono_font(20, bold=True)
    # Wrap long lines crudely at ~78 chars so nothing is cropped.
    wrapped: list[tuple[str, tuple, tuple | None, bool]] = []
    for text, fg, bg, bold in lines:
        if len(text) <= 78:
            wrapped.append((text, fg, bg, bold))
        else:
            for i in range(0, len(text), 78):
                wrapped.append((text[i : i + 78], fg, bg, bold))
    height = pad_top + pad_bottom + line_h * len(wrapped) + 16
    img = Image.new("RGB", (width, height), BG)
    d = ImageDraw.Draw(img)
    # Title bar dots + title.
    for i, col in enumerate([(255, 95, 86), (255, 189, 46), (39, 201, 63)]):
        d.ellipse([20 + i * 28, 20, 38 + i * 28, 38], fill=col)
    d.text((110, 18), title, font=title_font, fill=DIM)
    d.line([(0, 52), (width, 52)], fill=(40, 46, 60), width=2)
    y = pad_top
    for text, fg, bg, bold in wrapped:
        if bg is not None:
            d.rectangle([(8, y - 4), (width - 8, y + line_h - 4)], fill=bg)
        f = font_bold if bold else font
        d.text((pad_x, y), text, font=f, fill=fg)
        y += line_h
    img.save(path)
    return path


def draw_qr_block(d: ImageDraw.ImageDraw, x0: int, y0: int, size: int, seed: int = 7):
    """Draw an illustrative QR-like block (finder squares + deterministic modules)."""
    n = 25
    cell = size // n
    rnd = random.Random(seed)
    # White quiet zone + background.
    d.rectangle([(x0 - 12, y0 - 12), (x0 + size + 12, y0 + size + 12)], fill=WHITE)
    d.rectangle([(x0, y0), (x0 + size, y0 + size)], fill=WHITE)
    for r in range(n):
        for c in range(n):
            in_finder = (r < 8 and c < 8) or (r < 8 and c >= n - 8) or (r >= n - 8 and c < 8)
            if in_finder:
                continue
            if rnd.random() < 0.48:
                d.rectangle(
                    [(x0 + c * cell, y0 + r * cell), (x0 + (c + 1) * cell - 1, y0 + (r + 1) * cell - 1)],
                    fill=(0, 0, 0),
                )
    # Finder patterns.
    for fx, fy in [(0, 0), (n - 7, 0), (0, n - 7)]:
        x, y = x0 + fx * cell, y0 + fy * cell
        d.rectangle([(x, y), (x + 7 * cell - 1, y + 7 * cell - 1)], fill=(0, 0, 0))
        d.rectangle([(x + cell, y + cell), (x + 6 * cell - 1, y + 6 * cell - 1)], fill=WHITE)
        d.rectangle([(x + 2 * cell, y + 2 * cell), (x + 5 * cell - 1, y + 5 * cell - 1)], fill=(0, 0, 0))


def make_screenshots() -> dict[str, Path]:
    ASSETS.mkdir(parents=True, exist_ok=True)
    out: dict[str, Path] = {}

    out["overview"] = ASSETS / "01-tui-overview.png"
    draw_terminal(
        out["overview"],
        "qcode — main session",
        [
            ("[main*]  [agent-1]  [agent-2]      Ctrl+PgUp / PgDn to switch tabs", CYAN, None, True),
            ("", FG, None, False),
            ("> explain this repository", DIM, None, False),
            ("Reading README.md  ·  Writing docs/notes.md", GREEN, None, False),
            ("Done. I summarized the layout and next steps below.", FG, None, False),
            ("", FG, None, False),
            ("Queued #1: run the tests after the fix", YELLOW, PANEL, False),
            ("Working (*)  ·  1 queued  ·  input stays editable", CYAN, None, False),
            ("> add retry logic to the login flow", WHITE, None, True),
            ("remote off | MODE normal | MODEL qwen2.5-coder:7b | WS ~/demo", DIM, STATUS_BG, False),
            ("CTX 12% | STEP 3/32 | TOK I:1.2K O:340", DIM, STATUS_BG, False),
        ],
    )

    out["slash"] = ASSETS / "02-slash-help.png"
    draw_terminal(
        out["slash"],
        "qcode — slash commands",
        [
            ("> refactor the login handler", DIM, None, False),
            ("Completed in 00:42 (09/27 10:15)", GREEN, None, False),
            ("", FG, None, False),
            ("> /model  switch model (with search)", CYAN, SELECT_BG, True),
            ("  /plan    plan first, then build", FG, None, False),
            ("  /remote  control from a browser", FG, None, False),
            ("  /resume  reopen a saved session", FG, None, False),
            ("  /export  save transcript as HTML", FG, None, False),
            ("", FG, None, False),
            ("> /mo", WHITE, None, True),
            ("Type / to see suggestions. Tab completes. Esc closes.", DIM, None, False),
        ],
    )

    out["model"] = ASSETS / "03-model-picker.png"
    draw_terminal(
        out["model"],
        "qcode — /model picker",
        [
            ("Models | Type to filter | Up/Down, Enter to select", CYAN, None, True),
            ("", FG, None, False),
            ("> qwen2.5-coder:7b", WHITE, SELECT_BG, True),
            ("  qwen3:8b", FG, None, False),
            ("  gemma3", FG, None, False),
            ("  gpt-oss:20b", FG, None, False),
            ("", FG, None, False),
            ("List shows the current provider's model names only.", DIM, None, False),
            ("Thinking: off / low / medium / high / max (model decides list)", DIM, None, False),
        ],
    )

    out["agents"] = ASSETS / "04-agents.png"
    draw_terminal(
        out["agents"],
        "qcode — /agent list",
        [
            ("Agents (up to 20) | Enter switches, Ctrl+C leaves", CYAN, None, True),
            ("", FG, None, False),
            ("> main     qwen2.5-coder:7b  idle", WHITE, SELECT_BG, True),
            ("  agent-1  gpt-5             working", FG, None, False),
            ("  agent-2  kimi-k3           1 queued", YELLOW, None, False),
            ("", FG, None, False),
            ("/agent new | /agent switch <id> | /agent rename | /agent close", DIM, None, False),
            ("Each tab keeps its own history, draft, queue, and model.", DIM, None, False),
        ],
    )

    out["plan"] = ASSETS / "05-plan-mode.png"
    draw_terminal(
        out["plan"],
        "qcode — Plan mode",
        [
            ("(Plan)> investigate retry for the login flow", WHITE, None, True),
            ("PLAN in status bar: read-only investigation, no file changes", YELLOW, None, False),
            ("", FG, None, False),
            ("Plan submitted: summary + steps + checks", GREEN, None, False),
            ("  1. Add retry helper with backoff", FG, None, False),
            ("  2. Cover timeout + invalid token cases", FG, None, False),
            ("  3. Validate: run focused tests", FG, None, False),
            ("", FG, None, False),
            ("/plan show  review  |  /plan act  build it  |  /plan off  leave", CYAN, PANEL, False),
        ],
    )

    # Remote screen: terminal text on top; QR block drawn afterwards at fixed spot.
    remote_path = ASSETS / "06-remote-qr.png"
    draw_terminal(
        remote_path,
        "qcode — /remote (browser control)",
        [
            ("Remote control active | Pure Web - trusted LAN HTTP", CYAN, None, True),
            ("Address: http://192.168.1.10:42351", FG, None, False),
            ("Connections: 1 active browser session", FG, None, False),
            ("Remote login (scan or open link; single use, 3 minutes)", YELLOW, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("", FG, None, False),
            ("> Accept", WHITE, SELECT_BG, True),
            ("  Save QR as PNG", FG, None, False),
            ("  Close Connection", FG, None, False),
            ("QR PNG: /tmp/qcode-remote-XXXX.png (file is kept on disk)", GREEN, None, False),
        ],
    )
    # Overlay illustrative QR in the blank area.
    try:
        img = Image.open(remote_path)
        d = ImageDraw.Draw(img)
        # Blank area starts around line 6; place QR at left with caption space.
        draw_qr_block(d, 60, 200, 180, seed=11)
        d.text((280, 240), "Scan to open login link", font=mono_font(20), fill=DIM)
        d.text((280, 270), "Single use - valid 3 min", font=mono_font(20), fill=DIM)
        d.text((280, 300), "New /remote = new link", font=mono_font(20), fill=DIM)
        img.save(remote_path)
    except OSError:
        pass
    out["remote"] = remote_path

    out["diff"] = ASSETS / "07-diff-export.png"
    draw_terminal(
        out["diff"],
        "qcode — diff preview and export",
        [
            ("Writing internal/app.py (+12 -3)", CYAN, None, True),
            ("  @@ login handler @@", MAGENTA, None, False),
            ("    context = load_session()", FG, None, False),
            ("-   login_once(context)", RED, None, False),
            ("+   login_with_retry(context, attempts=3)", GREEN, None, False),
            ("", FG, None, False),
            ("/diff  expand  |  /diff 3  show third hunk (up to 200 lines)", DIM, None, False),
            ("", FG, None, False),
            ("> /export pretty", WHITE, None, True),
            ("Exported session to qcode-session-pretty-2026-09-27.html", GREEN, None, False),
        ],
    )

    out["interactive"] = ASSETS / "08-interactive-questions.png"
    draw_terminal(
        out["interactive"],
        "qcode — clarifying question",
        [
            ("> migrate the database", DIM, None, False),
            ("Which database should I use for this task?", YELLOW, None, True),
            ("", FG, None, False),
            ("> SQLite  (local file, zero setup)", WHITE, SELECT_BG, True),
            ("  Postgres (shared, needs connection)", FG, None, False),
            ("  Type a custom answer instead", FG, None, False),
            ("", FG, None, False),
            ("Up/Down moves, Enter answers, Ctrl+C skips. 1 of 3 max.", DIM, None, False),
            ("MODE INTERACTIVE in status bar | draft is kept while you answer", CYAN, None, False),
        ],
    )

    return out


# ---------------------------------------------------------------------------
# PDF manual (fpdf2)
# ---------------------------------------------------------------------------

SERIF = "/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf"
SERIF_B = "/usr/share/fonts/truetype/dejavu/DejaVuSerif-Bold.ttf"
SANS = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
SANS_B = "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"
SANS_O = "/usr/share/fonts/truetype/dejavu/DejaVuSans-Oblique.ttf"
MONO = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
MONO_B = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"


from fpdf.enums import XPos, YPos


class Manual(FPDF):
    def header(self):
        if self.page_no() == 1:
            return
        self.set_font("Sans", "", 8)
        self.set_text_color(110, 110, 120)
        self.cell(0, 8, "qcode User Manual  -  how to use qcode", align="L", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        self.ln(10)

    def footer(self):
        if self.page_no() == 1:
            return
        self.set_y(-15)
        self.set_font("Sans", "", 8)
        self.set_text_color(110, 110, 120)
        self.cell(0, 10, f"Page {self.page_no()}/{{nb}}", align="C")


def setup_fonts(pdf: Manual):
    import os

    def add(fam: str, style: str, path: str):
        if os.path.exists(path):
            pdf.add_font(fam, style, path)

    add("Serif", "", SERIF)
    add("Serif", "B", SERIF_B)
    add("Sans", "", SANS)
    add("Sans", "B", SANS_B)
    add("Sans", "I", SANS_O)
    add("Mono", "", MONO)
    add("Mono", "B", MONO_B)
    # Fallback check: if DejaVu missing, alias core fonts.
    try:
        pdf.set_font("Sans", "", 10)
    except RuntimeError:
        pass


def h1(pdf: Manual, title: str):
    pdf.set_font("Serif", "B", 18)
    pdf.set_text_color(20, 25, 35)
    pdf.start_section(title, level=0)
    pdf.multi_cell(0, 9, title, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.set_draw_color(60, 120, 200)
    pdf.set_line_width(0.6)
    pdf.line(pdf.l_margin, pdf.get_y(), pdf.w - pdf.r_margin, pdf.get_y())
    pdf.ln(4)


def h2(pdf: Manual, title: str):
    pdf.set_font("Sans", "B", 12)
    pdf.set_text_color(30, 60, 110)
    pdf.start_section(title, level=1)
    pdf.multi_cell(0, 7, title, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(1)


def body(pdf: Manual, text: str):
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    pdf.multi_cell(0, 5.8, text, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def bullets(pdf: Manual, items: list[str]):
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    for it in items:
        x0 = pdf.l_margin
        pdf.set_x(x0)
        pdf.cell(6, 5.8, chr(8226))
        pdf.multi_cell(0, 5.8, it, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def numbered(pdf: Manual, items: list[str]):
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    for i, it in enumerate(items, 1):
        x0 = pdf.l_margin
        pdf.set_x(x0)
        pdf.cell(8, 5.8, f"{i}.")
        pdf.multi_cell(0, 5.8, it, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def code(pdf: Manual, text: str):
    pdf.set_fill_color(243, 244, 246)
    pdf.set_font("Mono", "", 9)
    pdf.set_text_color(25, 25, 30)
    pdf.multi_cell(0, 5.4, text, fill=True, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(3)


def tip(pdf: Manual, text: str):
    pdf.set_fill_color(235, 245, 255)
    pdf.set_draw_color(90, 140, 210)
    pdf.set_font("Sans", "B", 10)
    pdf.set_text_color(30, 60, 110)
    pdf.cell(0, 6, "Tip", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    pdf.multi_cell(0, 5.8, text, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def figure(pdf: Manual, img: Path, caption: str, w: int = 170):
    if pdf.get_y() > 190:
        pdf.add_page()
    pdf.image(str(img), w=w, x=(210 - w) / 2)
    pdf.ln(2)
    pdf.set_font("Sans", "I", 9)
    pdf.set_text_color(90, 90, 100)
    pdf.multi_cell(0, 5, caption, align="C", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(3)


def cmd_table(pdf: Manual, rows: list[tuple[str, str]]):
    pdf.set_font("Sans", "B", 10)
    pdf.set_fill_color(30, 60, 110)
    pdf.set_text_color(255, 255, 255)
    pdf.cell(42, 7, "Command", fill=True)
    pdf.cell(0, 7, "What it does for you", fill=True, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.set_font("Sans", "", 9.5)
    pdf.set_text_color(35, 35, 35)
    fill = False
    for cmd, desc in rows:
        if fill:
            pdf.set_fill_color(242, 245, 250)
        else:
            pdf.set_fill_color(255, 255, 255)
        x0 = pdf.l_margin
        y0 = pdf.get_y()
        # Command cell on the left.
        pdf.set_xy(x0, y0)
        pdf.cell(42, 6.5, cmd, fill=True)
        # Description takes remaining width and may wrap.
        pdf.multi_cell(0, 6.5, desc, fill=True, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        fill = not fill
    pdf.ln(3)


def build_pdf(images: dict[str, Path]):
    pdf = Manual(orientation="P", unit="mm", format="A4")
    pdf.set_margins(15, 15, 15)
    pdf.set_auto_page_break(True, margin=20)
    pdf.alias_nb_pages("{nb}")
    setup_fonts(pdf)
    try:
        pdf.set_lang("en")
    except Exception:
        pass

    # Cover
    pdf.add_page()
    pdf.ln(28)
    pdf.set_font("Serif", "B", 34)
    pdf.set_text_color(20, 30, 50)
    pdf.multi_cell(0, 14, "qcode", align="C", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.set_font("Sans", "B", 20)
    pdf.set_text_color(40, 80, 140)
    pdf.multi_cell(0, 10, "User Manual", align="C", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(4)
    pdf.set_font("Sans", "", 11)
    pdf.set_text_color(80, 80, 90)
    pdf.multi_cell(0, 6, "A terminal-first AI coding assistant.\nThis guide shows how to use qcode day to day.", align="C", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(6)
    pdf.set_draw_color(60, 120, 200)
    pdf.set_line_width(0.8)
    pdf.line(70, pdf.get_y(), 140, pdf.get_y())
    pdf.ln(6)
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(60, 60, 70)
    pdf.multi_cell(0, 6, f"Version {VERSION}  -  {DATE}\nUsage guide for people using qcode.\nNo setup internals or code architecture inside.", align="C", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(10)
    pdf.set_font("Sans", "", 9)
    pdf.set_text_color(110, 110, 120)
    pdf.multi_cell(
        0,
        5.5,
        "In this manual: install once, tour the screen, ask and queue work,\n"
        "switch models, use agents, plan, skills, sessions, browser control, and settings.",
        align="C",
        new_x=XPos.LMARGIN, new_y=YPos.NEXT,
    )

    # How to use this manual
    pdf.add_page()
    h1(pdf, "How to use this manual")
    body(
        pdf,
        "Read chapters 1-3 to start. Keep chapters 4 and 15 nearby as a command "
        "reference. Chapters 5-12 are task guides you can read when you need them: "
        "models, agents, planning, skills, memory, web, sessions, and phone/browser control.",
    )
    bullets(
        pdf,
        [
            "Words in `code style` are things you type, for example `/model` or `qcode --demo`.",
            "Figures are example screens. Your colors and sizes may differ slightly.",
            "The prompt line shows `>` when idle, `(Plan)>` in Plan mode, and `(Queue)>` while busy.",
            "Esc goes back one level. Ctrl+C cancels without applying changes.",
        ],
    )
    tip(pdf, "Try everything first with: qcode --demo \"show me how qcode works\". Nothing is installed and your files are untouched.")

    # 1 Getting started
    h1(pdf, "1. Getting started")
    h2(pdf, "Install and first run")
    body(pdf, "qcode is one program you run in your terminal. Build it once, then start it in any project folder.")
    code(pdf, "# Build from source (needs Go 1.22+)\nmake build\n./bin/qcode")
    body(pdf, "Pick the assistant you want to use. The two common choices are a local model (Ollama) or a hosted model (OpenAI or OpenCode Go).")
    code(
        pdf,
        "# Local model (default provider)\nollama pull qwen2.5-coder:7b\nqcode --model qwen2.5-coder:7b\n\n# Hosted model\n"
        "export OPENAI_API_KEY=...\nqcode --provider openai --model gpt-5\n\n# No setup tour\nqcode --demo \"show me how qcode works\"",
    )
    tip(pdf, "If you only want to learn the keys and screens, start with --demo. It runs a scripted tour with fake tools and queued prompts.")
    h2(pdf, "Update")
    body(pdf, "Update the installed program from inside qcode when a new release is available.")
    code(pdf, "qcode update\nqcode update --arch arm64")

    # 2 Tour
    h1(pdf, "2. Touring the terminal")
    body(
        pdf,
        "qcode fills the terminal but keeps your normal scrollback. Tabs sit at the top, "
        "the conversation fills the middle, and the prompt plus status bar stay pinned at the bottom.",
    )
    figure(pdf, images["overview"], "Figure 1: Main screen. Tabs on top, transcript in the middle, queue notice, editable prompt, and status bar at the bottom.")
    h2(pdf, "What you see")
    bullets(
        pdf,
        [
            "Tabs: `main` is always there. Extra agents appear as new tabs (up to 20).",
            "Transcript: streamed answers with Markdown, tables, and tool activity like `Reading ...` or `Writing ...`.",
            "Queue area: appears only while busy. It lists waiting prompts in order.",
            "Prompt: `>` idle, `(Plan)>` planning, `(Queue)>` working. You can keep typing while work runs.",
            "Status bar: remote, mode, model, folder, context %, step, and tokens. Hide parts with `/statusline`.",
        ],
    )
    h2(pdf, "Keys you will use daily")
    bullets(
        pdf,
        [
            "Type `/` to see matching commands. Type more to filter, Tab to complete, Esc to close.",
            "Left/Right, Home/End move in the line. Ctrl+Left/Right jumps by word. Ctrl+W deletes a word. Ctrl+A/E jump to ends.",
            "PageUp/PageDown scrolls history, even while the agent works. Scrolling pauses the live view; PageDown to the bottom resumes it.",
            "Alt+Q expands a long queue so you can scroll it; Alt+Q again folds it back.",
            "Ctrl+C cancels the running prompt. Queued prompts wait their turn.",
        ],
    )
    bullets(
        pdf,
        [
            "Set NO_COLOR=1 to turn off colors. Set QCODE_ASCII=1 for plain-ASCII borders.",
        ],
    )

    # 3 Asking
    h1(pdf, "3. Asking, queueing, and history")
    body(pdf, "Type a request and press Enter. While one request runs, you can type the next one. It is marked `Queued #N` and runs automatically in order.")
    bullets(
        pdf,
        [
            "One prompt runs at a time per agent. Other agents are not blocked.",
            "The tab and task line show how many prompts are waiting.",
            "Use `/history` to find an old prompt. Pick one to re-read its final answer.",
            "Use `/new` to clear the current conversation without restarting qcode.",
            "Use `/compact` when a long session feels slow. It shortens stored context.",
        ],
    )
    tip(pdf, "Example flow: ask `explain this repository`, then while it works queue `run the tests after the fix`. Both complete in order without you waiting.")
    h2(pdf, "When qcode asks you back (/interactive)")
    body(
        pdf,
        "Normal work can pause to ask one focused question when an ambiguity would otherwise "
        "need several broad searches. Turn this on per agent with `/interactive on`, off with "
        "`/interactive off`, or check the setting with `/interactive`. It is off by default. "
        "Add `interactive = true` to config.toml to enable it for new terminal sessions.",
    )
    figure(pdf, images["interactive"], "Figure 2b: A clarifying question. Pick a suggestion or type your own. At most 3 questions per prompt.")
    bullets(
        pdf,
        [
            "One question at a time, with suggested choices or your own custom answer. At most 3 distinct questions per submitted prompt.",
            "Your answer becomes context for that agent only. It is not auto-saved; use `/learn` to keep it.",
            "The status bar shows MODE INTERACTIVE while enabled. Your half-typed draft is saved and restored around the question.",
            "Questions for other tabs wait on those tabs. Ctrl+C skips the question. The browser remote can also answer.",
            "One-shot prompts, piped input, and non-terminal runs never wait; qcode just proceeds with available context.",
        ],
    )
    tip(pdf, "Turn it on when tasks are ambiguous (which database, which scope). Leave it off for strict hands-off runs.")

    # 4 Commands
    h1(pdf, "4. Slash commands at a glance")
    body(pdf, "Type `/help` to list commands or `/help <name>` for one command. The leading `/` is optional in the name.")
    figure(pdf, images["slash"], "Figure 2: Type / to filter commands. Tab completes the first match.")
    cmd_table(
        pdf,
        [
            ("/help", "List commands or explain one command."),
            ("/new", "Clear conversation and token totals for this tab."),
            ("/resume", "Reopen a saved session for this folder."),
            ("/clear", "Redraw the welcome banner and tool summary."),
            ("/history", "Search completed prompts and re-read one answer."),
            ("/diff [N]", "Expand the latest file-change preview."),
            ("/verbose", "Show detailed telemetry for this session."),
            ("/maxsteps [N]", "Show or change the per-request step limit."),
            ("/statusline", "Show, hide, or reset status bar parts."),
            ("/compact", "Shorten conversation context manually."),
            ("/export", "Save transcript as readable or full HTML."),
            ("/quit, /exit", "Leave qcode."),
        ],
    )
    h2(pdf, "Diff previews")
    body(pdf, "When qcode writes or edits a file you see a short numbered preview with added and removed lines. Use `/diff` to expand it up to a safe limit. The model still receives plain text.")
    figure(pdf, images["diff"], "Figure 3: Numbered change preview plus /export pretty saving a readable HTML copy.")
    h2(pdf, "Exports")
    bullets(
        pdf,
        [
            "`/export` or `/export pretty`: one tab per agent, prompt cards that reveal answers. Best for sharing and reading.",
            "`/export raw`: full styled transcript including tool activity, thinking, errors, and diffs.",
            "Files are saved as `qcode-session-<mode>-<timestamp>.html`. A checkbox reveals detailed tool events.",
        ],
    )

    # 5 Models
    h1(pdf, "5. Choosing models and providers")
    body(pdf, "Use `/model` to switch the assistant for the current tab. The list shows model names from your current provider only, with no provider suffix. Type to search, move with Up/Down, Enter to pick. Some models ask a second question for thinking level.")
    figure(pdf, images["model"], "Figure 4: /model picker. Model names only; the list comes from the current provider. An optional thinking level follows (off/low/medium/high/max). Choices depend on the model.")
    h2(pdf, "Providers you can use")
    bullets(
        pdf,
        [
            "Ollama (local default): pull a model first, then `qcode --model qwen2.5-coder:7b`. Thinking uses the model's native think setting.",
            "OpenAI-compatible: `export OPENAI_API_KEY=...` then `qcode --provider openai --model gpt-5`. Thinking uses reasoning effort where the model offers it.",
            "OpenCode Go: `export QCODE_API_KEY=...` then `qcode --provider opencode-go --model kimi-k3`. Options vary per model.",
            "Flags can also come from QCODE_PROVIDER, QCODE_MODEL, QCODE_THINKING, QCODE_BASE_URL, QCODE_API_KEY.",
        ],
    )
    code(pdf, "qcode --provider ollama --model qwen3:8b --thinking high \"reply with OK\"\nqcode --provider opencode-go --model deepseek-v4-flash --thinking high \"reply with OK\"")
    tip(pdf, "On `main`, /model and /maxsteps remember your choice. Other tabs, demo, and one-shot runs keep changes for that session only.")
    h2(pdf, "One-shot and demo")
    code(pdf, "qcode \"explain this repository\"\nqcode --json-events \"run the tests\" 2>events.jsonl\nqcode --demo \"show me how qcode works\"")
    body(pdf, "Pass a prompt to run once without the interactive screen. Assistant text goes to stdout and short progress events go to stderr. Demo mode needs no model and touches no files.")

    # 6 Agents
    h1(pdf, "6. Working with multiple agents")
    body(pdf, "Use extra agents to do independent jobs at the same time, for example one writing code while another reads tests. Each tab keeps its own history, draft, queue, model, and tool choices.")
    figure(pdf, images["agents"], "Figure 5: /agent list. Enter switches to the highlighted agent.")
    cmd_table(
        pdf,
        [
            ("/agent", "Create an agent."),
            ("/agent list", "Pick and switch with Up/Down + Enter."),
            ("/agent switch <id>", "Jump directly to one agent."),
            ("/agent rename", "Give the current tab a clearer name."),
            ("/agent cancel", "Stop that agent's running work."),
            ("/agent close", "Remove that agent tab."),
        ],
    )
    bullets(
        pdf,
        [
            "Switch with Ctrl+PageUp/PageDown (Alt+, and Alt+. also work).",
            "Ask the main agent to split work and it can coordinate helpers; you can also create tabs yourself with /agent.",
            "Queues are per agent (up to 16 waiting each). A slow helper never blocks `main`.",
        ],
    )

    # 7 Plan
    h1(pdf, "7. Planning before coding")
    body(pdf, "Plan mode investigates without changing files. Enter it while idle, let qcode study the code, answer any design questions, review the submitted plan, then build it.")
    figure(pdf, images["plan"], "Figure 6: Plan mode. The prompt shows (Plan) and the status bar shows PLAN until you act or leave.")
    numbered(
        pdf,
        [
            "Type `/plan` to enter. The status bar shows PLAN.",
            "Describe the task. Answer focused questions if qcode asks (you can type a custom answer).",
            "Type `/plan show` to re-read the latest plan any time.",
            "Type `/plan act` to build the latest plan, or `/plan off` to leave without building.",
        ],
    )
    tip(pdf, "`/new` clears the saved plan. Plan commands need an idle agent with no running or queued work.")

    # 8 Skills
    h1(pdf, "8. Skills and SkillPlan")
    h2(pdf, "Using skills")
    body(pdf, "Skills are reusable instruction bundles, for example a team review checklist. Turn them on per tab with `/skill`. Only selected skills are shared with the assistant.")
    bullets(
        pdf,
        [
            "Type `/skill` to see names plus one-line summaries. Space selects, Enter applies.",
            "Your account, workspace, and configured folders can each provide skills.",
            "New files appear in `/skill` without restarting.",
            "A ready-made collection ships with qcode under docs/skills. Enable only what helps.",
        ],
    )
    h2(pdf, "Creating a skill safely")
    body(pdf, "Use `/skillplan` to turn a rough idea into a reviewed skill. It asks about purpose, workflow, inputs, outputs, and limits, then saves a draft for approval.")
    code(pdf, "/skillplan review database migrations before deployment")
    bullets(
        pdf,
        [
            "`/skillplan show` reviews the draft. `/skillplan create` writes it. `/skillplan off` leaves without writing.",
            "After creation, run `/skill` to enable the new skill.",
            "SkillPlan can read context but cannot run shell commands or change files until you approve creation.",
        ],
    )

    # 9 Learn
    h1(pdf, "9. Remembering preferences with /learn")
    body(pdf, "Use `/learn` after a good answer to save a durable preference or reusable procedure. qcode shows a preview and asks for approval. Nothing is saved until you answer `y` or `yes`.")
    cmd_table(
        pdf,
        [
            ("/learn", "Propose memory from recent conversation."),
            ("/learn list", "Show saved items and IDs."),
            ("/learn forget <id>", "Review then delete one item."),
            ("/learn compact", "Merge duplicates (asks first, backs up)."),
        ],
    )
    bullets(
        pdf,
        [
            "Memory is per user on this device and shared across folders.",
            "Keep entries reusable: state the language or condition when they apply.",
            "Review proposals: secrets are rejected, but you decide what is worth keeping.",
        ],
    )

    # 10 Web
    h1(pdf, "10. Web fetch and search")
    body(pdf, "Web tools read public pages and search results as text. They start OFF. Turn them on for the current session with `/tool`.")
    bullets(
        pdf,
        [
            "Type `/tool` and enable `web_fetch` and/or `web_search` independently.",
            "`/new` or restarting turns them off again. One-shot runs keep them off.",
            "Use them for docs and public references. They do not run page scripts and do not open a browser.",
        ],
    )
    tip(pdf, "If a page needs login or runs an app, fetch will not see it. Paste the relevant text into qcode instead.")

    # 11 Sessions
    h1(pdf, "11. Sessions: autosave, resume, and export")
    bullets(
        pdf,
        [
            "Sessions autosave while you work and on clean exit. Each launch starts its own session.",
            "Type `/resume` to reopen a session for this folder. Entries show a preview and age, newest first. Finish or cancel running work before switching.",
            "Empty and demo/one-shot runs are not saved. `/new` resets only the current tab inside its session.",
            "After a crash, qcode reopens the last good checkpoint and marks unfinished work interrupted. It never reruns tools by itself.",
            "Sessions live outside your project (for example ~/.local/state/qcode/sessions on Linux). They keep private file permissions.",
        ],
    )

    # 12 Remote
    h1(pdf, "12. Controlling qcode from a browser")
    body(pdf, "Type `/remote` to drive the same session from a phone or another browser. Pick Pure Web for a trusted local network or Tailscale for your tailnet. Avoid the `No auth` option unless you truly want an open short-lived demo.")
    figure(pdf, images["remote"], "Figure 7: /remote screen. Scan the code or open the link, then Accept. Save QR as PNG if the code is too tall for your window.")
    numbered(
        pdf,
        [
            "Type `/remote` and choose how to connect. For multiple networks, pick the interface to share.",
            "Scan the QR or open the shown link on the other device. Each link is single-use and lasts 3 minutes.",
            "If the code does not fit, choose `Save QR as PNG` and open the shown file path. The file is kept on disk.",
            "Choose Accept to keep the connection open. Run `/remote` again for one more device; existing browsers stay connected.",
            "To stop all browsers, choose Close Connection and confirm, or quit qcode.",
        ],
    )
    bullets(
        pdf,
        [
            "The browser shows the same tabs, queues, prompts, questions, and selectors as the terminal.",
            "Reloading the same browser tab resumes. A fresh browser needs a fresh link.",
            "In the browser, `/export` downloads the same HTML you get in the terminal.",
            "Use only on networks you trust. Pure Web is plain HTTP on your LAN.",
        ],
    )

    # 13 Config
    h1(pdf, "13. Settings you actually change")
    body(pdf, "Most daily choices live in the terminal (`/model`, `/maxsteps`, `/statusline`, `/tool`). Use a config file only for defaults you always want.")
    code(
        pdf,
        "provider = \"ollama\"\nmodel = \"qwen2.5-coder:7b\"\nmax_steps = 32\nsandbox = true",
    )
    bullets(
        pdf,
        [
            "Copy `config.toml.example` to your user config location. Missing files are ignored.",
            "Command flags beat environment variables. Both beat config files. Config files beat built-ins.",
            "Prefer environment variables for keys: QCODE_API_KEY or OPENAI_API_KEY. Keep key files private (mode 0600).",
            "Common knobs: thinking level, agent_timeout like \"5m\", interactive true/false, statusline_hidden, web_search backend, learning budget, skills paths.",
            "Restart after changing the web backend. `/model` and `/maxsteps` on `main` save automatically.",
        ],
    )

    # 14 Troubleshooting
    h1(pdf, "14. Troubleshooting and tips")
    cmd_table(
        pdf,
        [
            ("No colors?", "Run with NO_COLOR=1. For ASCII borders use QCODE_ASCII=1."),
            ("Narrow window?", "Status bar drops low-priority parts first, then wraps. Hide more with /statusline."),
            ("Slow session?", "Try /compact, then /new for a fresh tab context."),
            ("Need detail?", "Try /verbose. For step limits use /maxsteps N."),
            ("Lost output?", "Scroll with PageUp/PageDown. /history finds old answers. /export saves HTML."),
            ("Remote blocked?", "On Linux run once: sudo tailscale set --operator=$USER. Then retry /remote."),
        ],
    )
    tip(pdf, "Good defaults for most users: local model for drafts, stronger hosted model for final edits, Plan mode for risky changes, one agent per independent job.")

    # 15 Cheat sheet
    h1(pdf, "15. Cheat sheet (copy this page)")
    cmd_table(
        pdf,
        [
            ("/help", "Help for all or one command."),
            ("/model", "Switch model by name only (+ thinking)."),
            ("/agent ...", "Create, list, switch, close tabs."),
            ("/plan ...", "Plan, show, act, off."),
            ("/skill", "Enable instruction bundles."),
            ("/skillplan ...", "Draft and create a skill."),
            ("/interactive", "Let normal work ask up to 3 questions per prompt."),
            ("/learn ...", "Save/list/forget preferences."),
            ("/tool", "Toggle web fetch/search."),
            ("/resume, /new", "Reopen session / reset tab."),
            ("/history", "Find and re-read answers."),
            ("/diff", "Expand file-change preview."),
            ("/export", "Save pretty or raw HTML."),
            ("/remote", "Browser control via QR link."),
            ("/statusline", "Choose status bar parts."),
            ("/maxsteps", "Show/set step limit."),
            ("/clear, /quit", "Redraw banner / leave."),
        ],
    )
    body(pdf, "One-shot recipes:")
    code(
        pdf,
        "qcode --model qwen2.5-coder:7b \"explain this repository\"\n"
        "qcode --provider openai --model gpt-5 \"summarize the diff\"\n"
        "qcode --demo \"show me how qcode works\"",
    )

    # Back cover note
    pdf.add_page()
    h1(pdf, "About this manual")
    body(
        pdf,
        "This manual teaches use, not construction. It omits source layout, network internals, "
        "and developer extension points on purpose. For command details, type `/help` inside qcode. "
        f"Generated {DATE} as version {VERSION}. Screenshots are illustrative examples rendered for print clarity.",
    )

    # TOC at end? fpdf2 TOC must be inserted after sections are defined; easiest: insert near start.
    # We already used start_section, so create TOC page after cover by moving? Simplest: add TOC placeholder page now at end is wrong.
    # Workaround: fpdf2 allows insert_toc_placeholder anywhere; but sections already registered, so rendering at end still lists them.
    # Instead, we built without placeholder; add explicit chapter list here for reliability.
    h2(pdf, "Chapters in this manual")
    bullets(
        pdf,
        [
            "How to use this manual",
            "1. Getting started",
            "2. Touring the terminal",
            "3. Asking, queueing, and history",
            "4. Slash commands at a glance",
            "5. Choosing models and providers",
            "6. Working with multiple agents",
            "7. Planning before coding",
            "8. Skills and SkillPlan",
            "9. Remembering preferences with /learn",
            "10. Web fetch and search",
            "11. Sessions: autosave, resume, and export",
            "12. Controlling qcode from a browser",
            "13. Settings you actually change",
            "14. Troubleshooting and tips",
            "15. Cheat sheet",
        ],
    )

    pdf.output(str(PDF_PATH))
    print(f"Wrote {PDF_PATH} ({PDF_PATH.stat().st_size} bytes)")


def main():
    images = make_screenshots()
    for k, p in images.items():
        print(f"screenshot {k}: {p} ({p.stat().st_size} bytes)")
    build_pdf(images)


if __name__ == "__main__":
    main()
