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
import subprocess
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


def manual_version() -> str:
    """Match scripts/version.sh: exact tag or dev."""
    try:
        tag = subprocess.check_output(
            ["git", "describe", "--tags", "--exact-match", "HEAD"],
            stderr=subprocess.DEVNULL,
            cwd=ROOT.parent.parent,
        ).decode().strip()
        if tag:
            return tag
    except Exception:
        pass
    return "dev"


VERSION = manual_version()
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
PROMPT_BG = (38, 28, 50)

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
    # DejaVu Sans Mono lacks Braille spinner glyphs. Preserve the monospace
    # cell advance while drawing those symbols with the companion Sans font.
    symbol_font = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", 20)
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
        if any(0x2800 <= ord(char) <= 0x28FF for char in text):
            x = pad_x
            for char in text:
                chosen = symbol_font if 0x2800 <= ord(char) <= 0x28FF else f
                d.text((x, y), char, font=chosen, fill=fg)
                x += f.getlength(char)
        else:
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
            ("[main ● +1]  [agent-1 ○]  [agent-2 ○]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ explain this repository", MAGENTA, PROMPT_BG, True),
            ("", FG, None, False),
            ("> explain this repository", DIM, None, False),
            ("Reading README.md  ·  Writing docs/notes.md", GREEN, None, False),
            ("I am checking the entry points and tests.", FG, None, False),
            ("", FG, None, False),
            ("Steer", DIM, None, False),
            (" ╰─ focus on the login flow", FG, None, False),
            ("", FG, None, False),
            ("Queued | Alt+Q expand", DIM, None, False),
            (" ╰─ 1. run the login tests after the fix", FG, None, False),
            ("(Steer)> also check the timeout handling", WHITE, None, True),
            ("Waiting (⠋) · 1 queued  Ctrl+C to cancel", DIM, None, False),
            ("ollama [MODEL qwen2.5-coder:7b] [WS ~/demo] [CTX 12% left]", DIM, STATUS_BG, False),
            ("[STEP 3/32] [TOK I:1.2K O:340 Σ:1.5K]", DIM, STATUS_BG, False),
        ],
    )

    out["slash"] = ASSETS / "02-slash-help.png"
    draw_terminal(
        out["slash"],
        "qcode — slash commands",
        [
            ("[main ○]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ refactor the login handler", MAGENTA, PROMPT_BG, True),
            ("> refactor the login handler", DIM, None, False),
            ("Completed in 00:42 (09/27 10:15)", GREEN, None, False),
            ("", FG, None, False),
            ("/agent [new [name]|list|switch <id>|rename <id> <name>|...]", CYAN, None, True),
            ("/bash <cmd>  Run a shell command", FG, None, False),
            ("/clear       Clear the visible conversation", FG, None, False),
            ("/compact     Summarize old context", FG, None, False),
            ("/diff [N]    View more lines of a recent file change", FG, None, False),
            ("", FG, None, False),
            ("/model  Choose a model and optional thinking level", CYAN, SELECT_BG, True),
            ("> /mo", WHITE, None, True),
            ("ollama [MODEL qwen2.5-coder:7b] [WS ~/demo] [CTX 73% left]", DIM, STATUS_BG, False),
        ],
    )

    out["model"] = ASSETS / "03-model-picker.png"
    draw_terminal(
        out["model"],
        "qcode — /model picker",
        [
            ("[main ○]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ refactor the login handler", MAGENTA, PROMPT_BG, True),
            ("Select model (4/4) | Up/Down, PgUp/PgDn | Search: ", CYAN, None, True),
            ("", FG, None, False),
            ("> qwen2.5-coder:7b", WHITE, SELECT_BG, True),
            ("  qwen3:8b", FG, None, False),
            ("  gemma3", FG, None, False),
            ("  gpt-oss:20b", FG, None, False),
            ("", FG, None, False),
            ("> ", WHITE, None, True),
            ("ollama [MODEL qwen2.5-coder:7b] [WS ~/demo] [CTX 73% left]", DIM, STATUS_BG, False),
        ],
    )

    out["agents"] = ASSETS / "04-agents.png"
    draw_terminal(
        out["agents"],
        "qcode — /agent list",
        [
            ("[main ○]  [agent-1 ●]  [agent-2 ● +1]", CYAN, None, True),
            ("✦ summarize the authentication findings", MAGENTA, PROMPT_BG, True),
            ("Select agent (1/3) | Up/Down, PgUp/PgDn, Enter to switch", CYAN, None, True),
            ("", FG, None, False),
            ("> main      main               qwen2.5-coder:7b     idle", WHITE, SELECT_BG, True),
            ("    Login handler and test locations identified.", DIM, None, False),
            ("  agent-1   implementation     gpt-5                running", FG, None, False),
            ("  agent-2   review             kimi-k3              running (1 queued)", YELLOW, None, False),
            ("", FG, None, False),
            ("> ", WHITE, None, True),
            ("ollama [MODEL qwen2.5-coder:7b] [WS ~/demo] [CTX 73% left]", DIM, STATUS_BG, False),
        ],
    )

    out["plan"] = ASSETS / "05-plan-mode.png"
    draw_terminal(
        out["plan"],
        "qcode — Plan mode",
        [
            ("[main ○]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ investigate retry for the login flow", MAGENTA, PROMPT_BG, True),
            ("", FG, None, False),
            ("Plan submitted: summary + steps + checks", GREEN, None, False),
            ("  1. Add retry helper with backoff", FG, None, False),
            ("  2. Cover timeout + invalid token cases", FG, None, False),
            ("  3. Validate: run focused tests", FG, None, False),
            ("", FG, None, False),
            ("/plan show  review  |  /plan act  build it  |  /plan off  leave", CYAN, PANEL, False),
            ("(Plan)> ", WHITE, None, True),
            ("[MODE PLAN] ollama [MODEL qwen2.5-coder:7b] [WS ~/demo]", DIM, STATUS_BG, False),
        ],
    )

    # Remote screen: terminal text on top; QR block drawn afterwards at fixed spot.
    remote_path = ASSETS / "06-remote-qr.png"
    draw_terminal(
        remote_path,
        "qcode — /remote (browser control)",
        [
            ("[main ○]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ summarize the authentication findings", MAGENTA, PROMPT_BG, True),
            ("Remote control active | Pure Web · trusted LAN HTTP", CYAN, None, True),
            ("Address: http://192.168.1.10:42351", FG, None, False),
            ("Connections: 2 active browser sessions", FG, None, False),
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
        draw_qr_block(d, 60, 280, 180, seed=11)
        d.text((280, 320), "Scan to open login link", font=mono_font(20), fill=DIM)
        d.text((280, 350), "Single use - valid 3 min", font=mono_font(20), fill=DIM)
        d.text((280, 380), "New /remote = new link", font=mono_font(20), fill=DIM)
        img.save(remote_path)
    except OSError:
        pass
    out["remote"] = remote_path

    out["diff"] = ASSETS / "07-diff-export.png"
    draw_terminal(
        out["diff"],
        "qcode — diff preview and export",
        [
            ("[main ○]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ add retry logic to the login flow", MAGENTA, PROMPT_BG, True),
            ("Writing internal/app.py (+12 -3) · diff 1", CYAN, None, True),
            ("  @@ login handler @@", MAGENTA, None, False),
            ("    context = load_session()", FG, None, False),
            ("-   login_once(context)", RED, None, False),
            ("+   login_with_retry(context, attempts=3)", GREEN, None, False),
            ("", FG, None, False),
            ("/diff expands latest; /diff 3 expands saved diff 3 (to 200)", DIM, None, False),
            ("", FG, None, False),
            ("> /export pretty", DIM, None, False),
            ("Exported session to qcode-session-pretty-20260927-101500-", GREEN, None, False),
            ("000000001.html (custom paths are not accepted)", GREEN, None, False),
            ("> ", WHITE, None, True),
            ("ollama [MODEL qwen2.5-coder:7b] [WS ~/demo] [CTX 73% left]", DIM, STATUS_BG, False),
        ],
    )

    out["interactive"] = ASSETS / "08-interactive-questions.png"
    draw_terminal(
        out["interactive"],
        "qcode — clarifying question",
        [
            ("[main ●]    Ctrl+PgUp/PgDn · Alt+,/.", CYAN, None, True),
            ("✦ migrate the database", MAGENTA, PROMPT_BG, True),
            ("> migrate the database", DIM, None, False),
            ("Question 1/1:", YELLOW, None, True),
            ("Which database should I use for this task?", YELLOW, None, True),
            ("", FG, None, False),
            ("   1) SQLite - local file, zero setup", FG, None, False),
            ("   2) Postgres - shared, needs connection", FG, None, False),
            ("", FG, None, False),
            ("Choose an option or type your own answer.", DIM, None, False),
            ("Esc defers; Ctrl+C cancels active work.", DIM, None, False),
            ("Answer 1/1> 1", WHITE, None, True),
            ("Waiting (⠋) · Ctrl+C to cancel", DIM, None, False),
            ("[MODE INTERACTIVE] ollama [MODEL qwen2.5-coder:7b]", DIM, STATUS_BG, False),
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
    # Give numbered chapters a clear start; reserve space for their opening
    # content so a heading cannot be stranded at the bottom of a page.
    if (title[:1].isdigit() and pdf.get_y() > 35) or pdf.will_page_break(40):
        pdf.add_page()
    pdf.set_font("Serif", "B", 18)
    pdf.set_text_color(20, 25, 35)
    pdf.start_section(title, level=0)
    pdf.multi_cell(0, 9, title, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.set_draw_color(60, 120, 200)
    pdf.set_line_width(0.6)
    pdf.line(pdf.l_margin, pdf.get_y(), pdf.w - pdf.r_margin, pdf.get_y())
    pdf.ln(4)


def h2(pdf: Manual, title: str):
    if pdf.will_page_break(30):
        pdf.add_page()
    pdf.set_font("Sans", "B", 12)
    pdf.set_text_color(30, 60, 110)
    pdf.start_section(title, level=1)
    pdf.multi_cell(0, 7, title, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(1)


def body(pdf: Manual, text: str):
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    pdf.multi_cell(0, 5.4, text, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def bullets(pdf: Manual, items: list[str]):
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    for it in items:
        with pdf.unbreakable() as block:
            block.set_x(pdf.l_margin)
            block.cell(6, 5.4, chr(8226))
            block.multi_cell(0, 5.4, it, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def numbered(pdf: Manual, items: list[str]):
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    for i, it in enumerate(items, 1):
        x0 = pdf.l_margin
        pdf.set_x(x0)
        pdf.cell(8, 5.4, f"{i}.")
        pdf.multi_cell(0, 5.4, it, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def code(pdf: Manual, text: str):
    with pdf.unbreakable() as block:
        block.set_fill_color(243, 244, 246)
        block.set_font("Mono", "", 9)
        block.set_text_color(25, 25, 30)
        block.multi_cell(0, 5.4, text, fill=True, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        block.ln(3)


def tip(pdf: Manual, text: str):
    pdf.set_fill_color(235, 245, 255)
    pdf.set_draw_color(90, 140, 210)
    pdf.set_font("Sans", "B", 10)
    pdf.set_text_color(30, 60, 110)
    pdf.cell(0, 6, "Tip", new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.set_font("Sans", "", 10)
    pdf.set_text_color(35, 35, 35)
    pdf.multi_cell(0, 5.4, text, new_x=XPos.LMARGIN, new_y=YPos.NEXT)
    pdf.ln(2)


def figure(pdf: Manual, img: Path, caption: str, w: int = 170):
    with Image.open(img) as image:
        height = w * image.height / image.width
    if pdf.will_page_break(height + 22):
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
        "In this manual: install once, tour the screen, try practical use cases,\n"
        "clarifying questions, switch models, use agents, plan, skills, memory,\n"
        "web tools, sessions, browser control, and settings.",
        align="C",
        new_x=XPos.LMARGIN, new_y=YPos.NEXT,
    )

    # How to use this manual
    pdf.add_page()
    h1(pdf, "How to use this manual")
    body(
        pdf,
        "Read chapters 1-3 to start. Keep chapters 4 and 15 nearby as a command "
        "reference. The Use cases pages after chapter 3 offer complete example workflows. "
        "Chapters 5-12 are task guides you can read when you need them: "
        "models, agents, planning, skills, memory, web, sessions, and phone/browser control.",
    )
    bullets(
        pdf,
        [
            "Words in `code style` are things you type, for example `/model` or `qcode --demo`.",
            "Figures are illustrative example screens. Your colors and sizes may differ slightly.",
            "The prompt line shows `>` when idle, `(Plan)>` in Plan mode, `(Skill plan)>` in Skill Plan mode, and `(Steer)>` while busy.",
            "Esc goes back one level. Ctrl+C cancels without applying changes.",
        ],
    )
    tip(pdf, "Run qcode --demo for the interactive tour with automatic queued prompts. Add a quoted prompt for a one-shot text demo. Both use mocked tools and leave your workspace untouched.")

    # 1 Getting started
    h1(pdf, "1. Getting started")
    h2(pdf, "Install and first run")
    body(pdf, "qcode is one program you run in your terminal. Build it once, then start it in any project folder.")
    code(pdf, "# Build from source (needs Go 1.22+)\nmake build\n./bin/qcode")
    body(pdf, "Pick the assistant you want to use. The two common choices are a local model (Ollama) or a hosted model (OpenAI or OpenCode Go).")
    code(
        pdf,
        "# Local model (default provider)\nollama pull qwen2.5-coder:7b\nqcode --model qwen2.5-coder:7b\n\n# Hosted model\n"
        "export OPENAI_API_KEY=...\nqcode --provider openai --model gpt-5\n\n# No setup interactive tour\nqcode --demo",
    )
    tip(pdf, "If you only want to learn the keys and screens, start with --demo. It runs a scripted tour with fake tools and queued prompts.")
    h2(pdf, "Update")
    body(pdf, "Use the shell command below to install the latest release. It is a qcode CLI command, not a slash command.")
    code(pdf, "qcode update\nqcode update --arch arm64")

    # 2 Tour
    h1(pdf, "2. Touring the terminal")
    body(
        pdf,
        "qcode fills the terminal but keeps your normal scrollback. Tabs sit at the top, "
        "with the latest received prompt highlighted directly beneath them. The transcript "
        "scrolls in the middle; pending work, the editable composer, task indicator, and status bar stay at the bottom.",
    )
    figure(pdf, images["overview"], "Figure 1: Main screen. The highlighted prompt stays below the tabs; pending steering and queued tasks appear above the composer.")
    h2(pdf, "What you see")
    bullets(
        pdf,
        [
            "Tabs: `main` is always there. Extra agents appear as new tabs (up to 20 total).",
            "Latest prompt: a star marks up to three highlighted lines beneath the tabs. It changes when a task starts or steering is delivered. Pending input does not change it; /clear and completion keep it, /new clears it, and /resume restores it. Small windows show fewer lines.",
            "Transcript: streamed answers with Markdown, tables, and tool activity like `Reading ...` or `Writing ...`.",
            "Pending area: shows the current steer separately from FIFO tasks.",
            "Prompt: `>` idle, `(Plan)>` planning, `(Skill plan)>` skill design, `(Steer)>` working. You can keep typing while work runs.",
            "Status bar: provider, MODEL, WS folder, CTX percent left, STEP n/max, and TOK input/output plus total when it fits. THINK appears when set, MODE for planning or interactive questions, and REMOTE while browser control runs. Hide parts with /statusline.",
        ],
    )
    if pdf.will_page_break(85):
        pdf.add_page()
    h2(pdf, "Keys you will use daily")
    bullets(
        pdf,
        [
            "Type `/` to see up to five matching commands. Type more to filter, Tab to complete the first match, Esc to close.",
            "Left/Right edits input. At the bottom of output, Home/End move to input ends. Ctrl+Left/Right jumps by word; Ctrl+W deletes a word; Ctrl+A/E moves to input ends.",
            "PageUp/PageDown scrolls history with two overlapping rows of context, even while the agent works. Scrolling pauses the live view; PageDown to the bottom resumes it.",
            "When the view is above the bottom, Home/End jump to oldest/latest output. At the bottom they edit the prompt again. Browser text fields keep editing keys.",
            "Alt+Q opens pending work. Up/Down selects, PgUp/PgDn scrolls, Delete removes, and Esc closes.",
            "Ctrl+C cancels the running prompt. Queued prompts wait their turn.",
        ],
    )
    bullets(
        pdf,
        [
            "NO_COLOR=1 disables colors; QCODE_ASCII=1 uses ASCII glyphs.",
        ],
    )

    # 3 Asking
    h1(pdf, "3. Asking, queueing, and history")
    body(pdf, "Type a request and press Enter. While one request runs, you can type the next one. Enter steers the current task; Tab queues a separate task. Steering waits for a response or tool, replaces pending steering, and withdraws unanswered interactions.")
    body(pdf, "The pending panel separates Steer from Queued tasks with tree connectors. Use Alt+Q to expand it, Up/Down to select, Delete to remove an undelivered item, and Esc or Alt+Q to return to typing. Once steering starts replanning, it cannot be removed. Completed tool changes remain in place when you steer or cancel.")
    bullets(
        pdf,
        [
            "One prompt runs at a time per agent. Other agents are not blocked.",
            "The tab and task line show the queued task count.",
            "The newest pending steer replaces earlier pending steering. If the task ends before your steer is accepted, qcode rejects it and keeps the draft. Cancellation or failure drops undelivered steers; queued tasks still continue.",
            "Tab still completes matching slash commands. During manual /compact, Enter cannot steer and keeps your draft; Tab can queue a separate task.",
            "Use `/history` to find an old prompt. Pick one to re-read its final answer.",
            "Use `/new` to clear the current conversation without restarting qcode.",
            "Use `/compact` when a long session feels slow. It shortens stored context.",
        ],
    )
    tip(pdf, "Example: submit `fix the login timeout` with Enter. While it runs, submit `preserve the existing API` with Enter to steer it, or `run the login tests after the fix` with Tab to queue a separate task.")
    if pdf.will_page_break(125):
        pdf.add_page()
    h2(pdf, "When qcode asks you back (/interactive)")
    body(
        pdf,
        "Normal work can pause to ask one focused question when an ambiguity would otherwise "
        "need several broad searches. Turn this on per agent with `/interactive on`, off with "
        "`/interactive off`, or check the setting with `/interactive`. It is off by default. "
        "Add `interactive = true` to config.toml to enable it for new terminal sessions.",
    )
    figure(pdf, images["interactive"], "Figure 2: A clarifying question. Type an option number or your own answer. At most 3 questions per prompt.")
    bullets(
        pdf,
        [
            "One question at a time, with suggested choices or your own custom answer. At most 3 distinct questions per submitted prompt.",
            "Your answer becomes context for that agent and is included in saved sessions. It is not added to global learning automatically; use /learn to retain a reusable preference.",
            "The status bar shows [MODE INTERACTIVE] while enabled ([MODE INT] when narrow). Your half-typed draft is saved and restored around the question.",
            "Esc defers a question and opens the composer; Esc returns to it. A steer withdraws unanswered interactions. Ctrl+C cancels active work. The browser can also answer.",
            "One-shot prompts, piped input, non-terminal runs, and --json-events never wait; qcode just proceeds with available context.",
        ],
    )
    tip(pdf, "Turn it on when tasks are ambiguous (which database, which scope). Leave it off for strict hands-off runs.")

    # Practical recipes keep existing chapter numbers stable.
    pdf.add_page()
    h1(pdf, "Use cases: understand and fix")
    body(pdf, "These are example requests, not built-in commands. Replace paths, commands, and symptoms with those from your project. Review the resulting files and command output before accepting a change.")
    h2(pdf, "Understand an unfamiliar repository")
    body(pdf, "Goal: find the entry points and learn how to run the project before making changes.")
    numbered(pdf, [
        "Start qcode in the project folder and submit the example below.",
        "Ask a follow-up about the part you need to change. Use /history to revisit the explanation.",
        "Check the cited files and confirm the suggested commands against the project's README.",
    ])
    code(pdf, "Explain this repository without changing files. Identify the\nentry points, how to run it, and how to run its tests. Cite the\nfiles you used, and list anything you could not confirm.")
    body(pdf, "Result to look for: a file-grounded starting guide with clear run and test commands, plus open questions.")
    h2(pdf, "Fix a bug and queue the next step")
    body(pdf, "Goal: reproduce a specific failure, make a focused fix, and check the result.")
    numbered(pdf, [
        "Use /interactive on if expected behavior needs clarification; then describe the failure and a reproduction command.",
        "While the fix runs, press Tab to queue the follow-up below. It waits in the active agent's FIFO queue.",
        "Review the diff with /diff and inspect test output. Ask for an explanation if the checks fail.",
    ])
    code(pdf, "Fix the login timeout: retry once after a temporary network\nfailure, but never retry invalid credentials. Reproduce it\nwith the login tests before changing the handler.\n\n# Press Tab to queue while the first request is running:\nRun the login tests after the fix and summarize the results.")
    body(pdf, "Result to look for: a small diff and observed test results, with any remaining failure stated explicitly.")

    pdf.add_page()
    h1(pdf, "Use cases: plan and divide work")
    h2(pdf, "Plan a change before editing")
    body(pdf, "Goal: settle scope and review the approach before implementation.")
    numbered(pdf, [
        "On an idle agent, enter /plan and describe the change below.",
        "Answer design questions, then use /plan show to review the proposal. Request revisions if needed.",
        "Use /plan act only when ready to implement. Review the diff and the planned checks afterward; /plan off leaves without implementing.",
    ])
    code(pdf, "/plan\nPlan pagination for the search results. Inspect the current\nAPI and UI, identify compatibility concerns, and propose\nacceptance checks before changing files.\n/plan show\n/plan act")
    body(pdf, "Result to look for: an agreed plan followed by changes that meet its acceptance checks.")
    h2(pdf, "Use independent agents for implementation and investigation")
    body(pdf, "Goal: keep a focused implementation moving while another tab investigates a separate question.")
    numbered(pdf, [
        "Give main the implementation task. Use /agent new to pick a model for a helper; rename its displayed ID with /agent rename <id> review-tests.",
        "In the helper tab, ask for a read-only review of relevant tests and missing edge cases. Switch tabs with Ctrl+PageUp/PageDown.",
        "Bring the helper's findings back to main, request the needed checks, and review the combined result.",
    ])
    code(pdf, "# Main prompt:\nAdd the agreed pagination behavior to the search endpoint.\n\n# Helper prompt:\nRead the search tests without changing files. Identify\nmissing pagination edge cases and report the file locations.")
    tip(pdf, "Agents share the workspace. Give them distinct responsibilities; avoid asking two tabs to edit the same files at once. Each tab has its own history and queue.")

    pdf.add_page()
    h1(pdf, "Use cases: reuse and continue")
    h2(pdf, "Turn a repeated review into a skill")
    body(pdf, "Goal: make a repeatable checklist available in future work.")
    numbered(pdf, [
        "Start /skillplan with the example below and answer questions about scope, inputs, and validation.",
        "Review with /skillplan show; request edits until the draft describes your actual workflow.",
        "Use /skillplan create to write the reviewed skill, then /skill to enable it before the next review.",
    ])
    code(pdf, "/skillplan Create a database migration review checklist.\nCheck rollback behavior, existing data compatibility,\nand validation commands. Report risks with file references.\n/skillplan show\n/skillplan create\n/skill")
    body(pdf, "Result to look for: an enabled, reusable skill whose instructions and completion checks you have reviewed.")
    h2(pdf, "Resume work and share its outcome")
    body(pdf, "Goal: continue a saved conversation and produce a readable handoff.")
    numbered(pdf, [
        "Return to the same project folder, start qcode, and use /resume to choose the saved session.",
        "Use /history to find the last completed answer. Ask for current status and remaining work before continuing; interrupted tools are not rerun automatically.",
        "Use /export pretty for a shareable HTML conversation, or /export raw when the handoff needs detailed activity.",
    ])
    code(pdf, "/resume\nSummarize the completed changes and remaining work. Verify\nthe current files before continuing the unfinished task.\n/export pretty")
    tip(pdf, "To continue from a phone on a trusted network, use /remote and redeem the QR link. The browser controls the same live session; its /export downloads the HTML. Close the connection when finished.")

    # 4 Commands
    h1(pdf, "4. Slash commands at a glance")
    body(pdf, "Type `/help` to list commands or `/help <name>` for one command. The leading `/` is optional in the name. Agent, skill, learning, plan, remote, model, tool, and interactive commands are covered in their chapters below.")
    figure(pdf, images["slash"], "Figure 3: /help lists commands. Typing /mo filters the suggestions to /model; Tab completes the first match.")
    cmd_table(
        pdf,
        [
            ("/help", "List commands or explain one command."),
            ("/bash <cmd>", "Run a shell command in the workspace."),
            ("/new", "Start a fresh conversation for the active agent."),
            ("/resume", "Continue a saved session (picker or session-id)."),
            ("/clear", "Clear the visible conversation and redraw the header."),
            ("/history", "Browse completed prompts and responses."),
            ("/diff [N]", "View more lines of saved diff N; omit N for latest."),
            ("/verbose", "Show or hide detailed action traces."),
            ("/maxsteps [N]", "Show or change the model-turn limit."),
            ("/statusline", "Show, hide, or reset status bar parts."),
            ("/theme", "Preview, apply, and save a terminal color theme."),
            ("/compact", "Summarize old context to make room."),
            ("/export", "Export conversations or full transcript as HTML."),
            ("/quit, /exit", "Save the session and exit qcode."),
        ],
    )
    h2(pdf, "Diff previews")
    body(pdf, "When qcode writes or edits a file you see a short numbered 10-line preview with added and removed lines. Use `/diff` to expand the latest saved diff or `/diff N` to expand saved diff N, up to the 200-line safety limit. The model still receives plain text.")
    figure(pdf, images["diff"], "Figure 4: Numbered change preview plus /export pretty saving a readable HTML copy.")
    h2(pdf, "Exports")
    bullets(
        pdf,
        [
            "`/export` or `/export pretty`: one tab per agent, prompt cards that reveal answers. Best for sharing and reading.",
            "`/export raw`: full styled transcript including tool activity, thinking, errors, and diffs.",
            "Files are saved as `qcode-session-<mode>-<timestamp>.html` with subsecond precision. Custom output paths are not accepted.",
        ],
    )

    # 5 Models
    h1(pdf, "5. Choosing models and providers")
    body(pdf, "Use `/model` to switch the assistant for the current tab. The list shows model names from your current provider only, with no provider suffix. Type to search, move with Up/Down and PgUp/PgDn, Enter to pick. You can also run `/model <id> [thinking]` directly. Some models ask a second question for thinking level; Esc returns to the model list and Ctrl+C cancels.")
    figure(pdf, images["model"], "Figure 5: /model picker. Model names only; the list comes from the current provider. An optional thinking level follows (off/low/medium/high/max). Choices depend on the model.")
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
    tip(pdf, "On `main`, /model and /maxsteps remember your choice. Other tabs, demo, one-shot, and piped runs keep changes for that session only.")
    h2(pdf, "One-shot and demo")
    code(pdf, "qcode \"explain this repository\"\nqcode --json-events \"run the tests\" 2>events.jsonl\nqcode --demo \"show me how qcode works\"")
    body(pdf, "Pass a prompt to run once without the interactive screen. Assistant text goes to stdout and short progress events go to stderr. Demo mode needs no model and touches no files.")

    # 6 Agents
    h1(pdf, "6. Working with multiple agents")
    body(pdf, "Use extra agents to do independent jobs at the same time, for example one writing code while another reads tests. Each tab keeps its own history, draft, queue, model, and tool choices.")
    figure(pdf, images["agents"], "Figure 6: /agent list. Enter switches to the highlighted agent.")
    cmd_table(
        pdf,
        [
            ("/agent", "Create an agent (same as /agent new)."),
            ("/agent new [model]", "Create an agent, optionally with a model ID."),
            ("/agent list", "Pick and switch with Up/Down + Enter."),
            ("/agent switch <id>", "Jump directly to one agent."),
            ("/agent rename <id> <name>", "Give an agent a new display name."),
            ("/agent cancel <id>", "Stop that agent's current work."),
            ("/agent close <id>", "Close that agent tab (--yes skips confirm)."),
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
    figure(pdf, images["plan"], "Figure 7: Plan mode. The prompt shows (Plan) and the status bar shows [MODE PLAN] until you act or leave.")
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
    body(pdf, "Skills are reusable instruction bundles, for example a team review checklist. Turn them on per tab with `/skill`. Only selected skills are shared with the assistant. You can also run `/skill name1,name2` directly, or `/skill none` to clear them.")
    bullets(
        pdf,
        [
            "Type `/skill` to see names plus one-line summaries. Type to filter, Up/Down or PgUp/PgDn to move, Space to toggle, Enter to apply, Esc to leave, Ctrl+C to cancel.",
            "Your account, workspace, and configured folders can each provide skills.",
            "New files appear in `/skill` without restarting.",
            "A ready-made collection ships with qcode under docs/skills. Enable only what helps.",
        ],
    )
    h2(pdf, "Creating a skill safely")
    body(pdf, "Use `/skillplan` alone or with a rough idea to turn it into a reviewed skill. It asks when the skill applies, its workflow, inputs and outputs, constraints, validation, name, and scope, with follow-ups when an answer leaves a decision open.")
    code(pdf, "/skillplan review database migrations before deployment")
    bullets(
        pdf,
        [
            "`/skillplan show` reviews the draft. `/skillplan create` writes it. `/skillplan off` leaves without writing.",
            "Names allow 1-64 lowercase letters, digits, hyphens, or underscores; files must fit 64 KiB. qcode never overwrites an existing SKILL.md.",
            "After creation, run `/skill` to enable the new skill.",
            "SkillPlan can read context but cannot run shell commands or change files until you approve creation.",
        ],
    )

    # 9 Learn
    h1(pdf, "9. Remembering preferences with /learn")
    body(pdf, "Use `/learn` after a good answer to save a durable preference or reusable procedure. qcode shows a preview and asks `Apply these global learning changes? [y/N]`. Nothing is saved unless you answer `y` or `yes`; Enter, `n`, or Ctrl+C cancels.")
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
    body(pdf, "Web tools read public pages and search results as text. They start OFF. Turn them on for the current session with `/tool`. The backend setting alone does not enable them.")
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
            "Sessions autosave about every two seconds, after agent work and commands, and on clean exit or session switch. Each launch starts its own session.",
            "Type /resume to reopen a session for this folder. Entries show a preview and age, newest first. Finish or cancel running work before switching. Sessions open in another qcode process cannot be restored; Enter retries the selected session after a lock or snapshot error.",
            "Resume restores tabs, models, the pinned latest prompt, conversation and styled output, diffs, drafts, reading positions, tool and skill settings, and token totals. Provider credentials come from current configuration.",
            "Empty and demo/one-shot runs are not saved. `/new` resets only the current tab inside its session.",
            "After a crash, start qcode in the same folder and use /resume. The last good checkpoint marks unfinished work interrupted; pending tasks and steering are never resubmitted automatically. Inspect current files before continuing, since completed tool changes are not rolled back.",
            "Sessions live outside your project: ~/.local/state/qcode/sessions on Linux (or $XDG_STATE_HOME), ~/Library/Application Support on macOS, %AppData% on Windows. They keep private file permissions.",
        ],
    )
    h2(pdf, "Local privacy and redaction")
    body(pdf, "Local text filtering is on by default. Detected credentials, private keys, payment-card numbers, and labelled card security codes or expiry dates appear as [REDACTED] in output, thinking, tool previews, saved sessions, exports, and browser views. Live editable input stays visible while you compose it; submitted history and saved draft copies are filtered.")
    body(pdf, "When you resume an older session, qcode filters and rewrites it before showing the restored conversation. If that rewrite fails, resume stops. Redacted saved text cannot be recovered, so the resumed conversation may need missing context supplied again.")
    body(pdf, "Filtering applies to local text, not requests sent to a model provider, shell commands, workspace file writes, or image contents. Detection can miss unfamiliar formats. Use [redaction] settings for custom patterns, fields, and paths; see docs/privacy.md and docs/configuration.md for coverage and options.")

    # 12 Remote
    h1(pdf, "12. Controlling qcode from a browser")
    body(pdf, "Type /remote to control the same session from a phone or browser. Choose Pure Web for a trusted LAN or Tailscale for your tailnet. No auth gives anyone with the URL access; reserve it for open, short-lived demos.")
    figure(pdf, images["remote"], "Figure 8: /remote screen. Scan or open the link, then Accept. Save QR as PNG if it does not fit.", w=160)
    numbered(
        pdf,
        [
            "Type `/remote` and choose how to connect. For multiple networks, pick the interface to share.",
            "Scan or open the link on the other device. Links are single-use and last 3 minutes. A new link replaces an unused one; existing browsers stay connected.",
            "If the code does not fit, choose `Save QR as PNG` and open the shown file path (in WSL, convert it with `wslpath -w <path>`). The file is kept on disk.",
            "Choose Accept to keep the connection open. Run `/remote` again for one more device; existing browsers stay connected.",
            "To stop all browsers, choose Close Connection and confirm, or quit qcode. Used and expired links tell you to run `/remote` again.",
        ],
    )
    bullets(
        pdf,
        [
            "Browser Enter and Steer update the busy task; Queue submits a separate task. Tab navigates controls normally.",
            "Remove cancels pending input; Cancel active work stops the running task. Rejected submissions keep your draft.",
            "The selected agent's latest received prompt stays below the browser header, up to three lines, while the transcript scrolls. It updates only when a task starts or steering is delivered.",
            "Beginning and Latest navigate transcript boundaries. Home/End do the same outside text fields, command panels, and the expanded pending list; text fields retain their normal editing keys.",
            "Reloading the same browser tab resumes access; a fresh browser needs a fresh link. /export downloads the same HTML as in the terminal.",
            "Use only on networks you trust. Pure Web is plain HTTP on your LAN.",
        ],
    )

    # 13 Config
    h1(pdf, "13. Settings you actually change")
    body(pdf, "Most daily choices live in the terminal (`/model`, `/maxsteps`, `/statusline`, `/theme`, `/tool`). Use a config file only for defaults you always want.")
    h2(pdf, "Terminal colors and workspace visibility")
    body(pdf, "Use /theme to preview terminal palettes with Up/Down. Enter applies and saves the choice; Esc or Ctrl+C cancels. Default (Auto) follows your terminal. Dark and light choices include Catppuccin, Dracula/Alucard, Gruvbox, Solarized, and Nord. The setting is terminal-wide and saves from any agent tab. The browser's pinned prompt follows the theme's accent color and background.")
    body(pdf, "WS uses available status bar space and keeps the final two complete folders when possible, using a second row as needed. Home-relative paths retain ~/; omitted parents appear as an ellipsis folder, for example ~/.../work/foo. If the final folder name must be clipped, its ending gets an ellipsis: ~/workspace/this-folder-is... (the final dots shorten the name).")
    body(pdf, "TOK shows provider-reported session input/output counts. The combined total uses a sigma marker, or T: in ASCII mode, whenever it fits one or two status rows. If usage is missing, unknown or a question mark indicates incomplete counts. /new resets the active agent's totals; changing models keeps them.")
    body(pdf, "Open /statusline, move with Up/Down, toggle with Space, and apply with Enter. Or type /statusline tok off, /statusline show, or /statusline reset. Changes on main save statusline_hidden in your user config while preserving comments and unrelated tables, including redaction and skills settings.")
    code(
        pdf,
        "provider = \"ollama\"\nmodel = \"qwen2.5-coder:7b\"\nmax_steps = 32\ntheme = \"catppuccin-mocha\"\nsandbox = true",
    )
    bullets(
        pdf,
        [
            "Copy `config.toml.example` to your user config location; missing files are ignored. Priority, highest first: file beside the program, user config (~/.local/etc/qcode on Linux, ~/Library/Application Support on macOS, %AppData% on Windows), system config (/usr/local/etc/qcode), then etc/ beside the program.",
            "Command flags beat environment variables. Both beat config files. Config files beat built-ins. `--api-key` beats config and environment keys.",
            "Prefer environment variables for keys: QCODE_API_KEY or OPENAI_API_KEY. Keep key files private (mode 0600).",
            "Common knobs: thinking level, agent_timeout like \"5m\", interactive true/false, statusline_hidden, web_search backend, learning budget, skills paths.",
            "Restart after changing the web backend. /model, /maxsteps, and /statusline on main save automatically; worker-tab changes to these settings stay within the session.",
        ],
    )

    # 14 Troubleshooting
    h1(pdf, "14. Troubleshooting and tips")
    cmd_table(
        pdf,
        [
            ("No colors?", "Unset NO_COLOR to enable colors. Set NO_COLOR=1 to disable them; QCODE_ASCII=1 uses ASCII glyphs while retaining color."),
            ("Narrow window?", "The path shortens first; the bar wraps to a second left-aligned line. Low-priority parts drop only if two lines still overflow. Hide more with /statusline."),
            ("Slow session?", "Try /compact, then /new for a fresh tab context."),
            ("Need detail?", "Try /verbose. For step limits use /maxsteps N."),
            ("Lost output?", "Scroll with PageUp/PageDown. /history finds old answers. /export saves HTML."),
            ("Resume failed?", "Close another process using that session, then press Enter to retry. Snapshot or save errors must be resolved before switching."),
            ("Text is redacted?", "Detected sensitive text is replaced locally by [REDACTED]. Saved redactions are permanent; review docs/privacy.md for scope."),
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
            ("/bash <cmd>", "Run a shell command."),
            ("/model", "Switch model by name only (+ thinking)."),
            ("/agent ...", "Create, list, switch, rename, cancel, close."),
            ("/plan ...", "Plan, show, act, off."),
            ("/skill", "Enable instruction bundles."),
            ("/skillplan ...", "Draft, show, create, or leave skill design."),
            ("/interactive", "Let normal work ask up to 3 questions per prompt."),
            ("/learn ...", "Save/list/forget/compact preferences."),
            ("/tool", "Toggle web fetch/search."),
            ("/resume, /new", "Reopen session / reset tab."),
            ("/history", "Find and re-read answers."),
            ("/diff", "Expand saved diffs."),
            ("/compact", "Summarize old context."),
            ("/verbose", "Show/hide detailed traces."),
            ("/export", "Save pretty or raw HTML."),
            ("/remote", "Browser control via QR link."),
            ("/statusline", "Choose status bar parts."),
            ("/theme", "Preview and save terminal colors."),
            ("/maxsteps", "Show/set step limit."),
            ("/clear", "Clear view and redraw header."),
            ("/quit, /exit", "Save session and exit."),
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
            "Use cases: understand and fix; plan and divide work; reuse and continue",
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
