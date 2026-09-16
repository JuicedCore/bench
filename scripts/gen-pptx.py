#!/usr/bin/env python3
"""Generate a 16:9 campaign PowerPoint from per-run monitoring reports.

    python3 scripts/gen-pptx.py --campaign results/_campaigns/<id>
    python3 scripts/gen-pptx.py --campaign <dir> --out /path/to/deck.pptx

Resolves each platform's result directory from SUMMARY.tsv plus
"results written to …" in that step's run.log. Tables are filled from
sibling result.json (same payload the HTML was built from). Charts are the
PNGs already embedded as data:image/png;base64 in monitoring-report.html.

Explainer slides are static templates (methodology, load path, semantics);
those pages are not scraped at generation time.
"""
from __future__ import annotations

import argparse
import base64
import csv
import html
import io
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

try:
    from pptx import Presentation
    from pptx.dml.color import RGBColor
    from pptx.enum.shapes import MSO_SHAPE
    from pptx.enum.text import MSO_ANCHOR, PP_ALIGN
    from pptx.oxml.ns import qn
    from pptx.util import Inches, Pt
except ImportError:
    sys.exit("pip install python-pptx")

try:
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
except ImportError:
    sys.exit("pip install matplotlib")

# --- theme -------------------------------------------------------------------
# Widescreen 16:9, dark charcoal, one blue accent. No gradients/clipart/emojis.

W, H = Inches(13.333333), Inches(7.5)
BG = RGBColor(0x1A, 0x1D, 0x23)
CARD = RGBColor(0x22, 0x26, 0x2E)
CARD_ALT = RGBColor(0x28, 0x2C, 0x35)
ACCENT = RGBColor(0x3D, 0x8B, 0xFF)
TEXT = RGBColor(0xE8, 0xEC, 0xF1)
MUTED = RGBColor(0x8B, 0x96, 0xA4)
LINE = RGBColor(0x2A, 0x31, 0x3C)
AMBER = RGBColor(0xE0, 0xA0, 0x4A)
AMBER_BG = RGBColor(0x3A, 0x2F, 0x24)
BOUND_BG = RGBColor(0x24, 0x2C, 0x3A)
WHITE = RGBColor(0xFF, 0xFF, 0xFF)
FONT = "Calibri"
FONT_NUM = "Consolas"

RESULTS_WRITTEN = re.compile(r"results written to (results/[^\s)]+)")
# Full base64 until the closing quote — do not cap the lookahead; these PNGs are large.
CHART_BLOCK = re.compile(
    r'<div class="chart-title">(.*?)</div>\s*'
    r'(?:<img\s+src="data:image/png;base64,([^"]+)"|<div class="placeholder")',
    re.DOTALL,
)

# Preferred resource-chart titles from the monitoring report (skip placeholders).
CHART_PREFER = (
    ("host_cpu", ("Host CPU utilisation", "Host CPU")),
    ("container_cpu", ("Container CPU cores", "Container CPU")),
    ("container_mem", ("Container memory",)),
)
CHART_FALLBACK = (
    ("host_cpu", ("Harness CPU sampling", "Harness CPU")),
    ("container_mem", ("Harness memory sampling", "Harness memory")),
)


# --- tiny helpers ------------------------------------------------------------

def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def rgb_fill(shape, color: RGBColor) -> None:
    shape.fill.solid()
    shape.fill.fore_color.rgb = color


def no_line(shape) -> None:
    shape.line.fill.background()


def set_run(run, *, size, color, bold=False, name=FONT, align=None) -> None:
    run.font.size = Pt(size)
    run.font.color.rgb = color
    run.font.bold = bold
    run.font.name = name
    if align is not None:
        run._r.getparent().set("algn", align)


def add_text(slide, left, top, width, height, text, *, size=18, color=TEXT,
             bold=False, name=FONT, align=PP_ALIGN.LEFT, anchor=MSO_ANCHOR.TOP):
    box = slide.shapes.add_textbox(left, top, width, height)
    tf = box.text_frame
    tf.word_wrap = True
    tf.auto_size = None
    try:
        tf._txBody.find(qn("a:bodyPr")).set("anchor", {
            MSO_ANCHOR.TOP: "t",
            MSO_ANCHOR.MIDDLE: "ctr",
            MSO_ANCHOR.BOTTOM: "b",
        }.get(anchor, "t"))
    except Exception:
        pass
    p = tf.paragraphs[0]
    p.alignment = align
    run = p.add_run()
    run.text = text
    run.font.size = Pt(size)
    run.font.color.rgb = color
    run.font.bold = bold
    run.font.name = name
    return box


def add_paras(slide, left, top, width, height, lines, *, size=16, color=TEXT,
              bold=False, spacing=8):
    """Body copy. Truncates to 6 lines (one idea / ≤6 bullets)."""
    lines = list(lines)[:6]
    box = slide.shapes.add_textbox(left, top, width, height)
    tf = box.text_frame
    tf.word_wrap = True
    for i, line in enumerate(lines):
        p = tf.paragraphs[0] if i == 0 else tf.add_paragraph()
        p.alignment = PP_ALIGN.LEFT
        p.space_after = Pt(spacing)
        run = p.add_run()
        run.text = line
        run.font.size = Pt(size)
        run.font.color.rgb = color
        run.font.bold = bold
        run.font.name = FONT
    return box


def card(slide, left, top, width, height, fill=CARD):
    sh = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, left, top, width, height)
    rgb_fill(sh, fill)
    sh.line.color.rgb = LINE
    sh.line.width = Pt(1)
    return sh


def flow_box(slide, left, top, width, height, text, *, accent=False):
    sh = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, left, top, width, height)
    rgb_fill(sh, ACCENT if accent else CARD)
    sh.line.color.rgb = ACCENT
    sh.line.width = Pt(1.25)
    tf = sh.text_frame
    tf.word_wrap = True
    try:
        tf._txBody.find(qn("a:bodyPr")).set("anchor", "ctr")
    except Exception:
        pass
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.CENTER
    run = p.add_run()
    run.text = text
    run.font.size = Pt(11)
    run.font.color.rgb = WHITE if accent else TEXT
    run.font.bold = True
    run.font.name = FONT
    return sh


def arrow(slide, left, top, width, height=None):
    h = height if height is not None else Inches(0.18)
    sh = slide.shapes.add_shape(MSO_SHAPE.RIGHT_ARROW, left, top, width, h)
    rgb_fill(sh, ACCENT)
    no_line(sh)
    return sh


def new_slide(prs):
    layout = prs.slide_layouts[6] if len(prs.slide_layouts) > 6 else prs.slide_layouts[-1]
    slide = prs.slides.add_slide(layout)
    bg = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, 0, 0, prs.slide_width, prs.slide_height)
    rgb_fill(bg, BG)
    no_line(bg)
    bar = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, 0, 0, prs.slide_width, Inches(0.07))
    rgb_fill(bar, ACCENT)
    no_line(bar)
    return slide


def kicker_title(slide, kicker, title, *, subtitle=None):
    add_text(slide, Inches(0.55), Inches(0.22), Inches(12.2), Inches(0.28),
             kicker, size=12, color=ACCENT, bold=True)
    add_text(slide, Inches(0.55), Inches(0.46), Inches(12.2), Inches(0.5),
             title, size=26, color=TEXT, bold=True)
    if subtitle:
        add_text(slide, Inches(0.55), Inches(0.92), Inches(12.2), Inches(0.35),
                 subtitle, size=14, color=MUTED)


def finalize_footers(prs, campaign_id: str) -> None:
    n = len(prs.slides)
    for i, slide in enumerate(prs.slides, 1):
        add_text(slide, Inches(0.55), Inches(7.18), Inches(9.5), Inches(0.24),
                 campaign_id, size=10, color=MUTED, name=FONT_NUM)
        add_text(slide, Inches(11.2), Inches(7.18), Inches(1.55), Inches(0.24),
                 f"{i}  /  {n}", size=10, color=MUTED, name=FONT_NUM, align=PP_ALIGN.RIGHT)


# --- numbers -----------------------------------------------------------------

def pctl(snap, name: str):
    if not snap:
        return None
    return (snap.get("percentiles_ms") or {}).get(name)


def fmt_tps(x) -> str:
    if x is None:
        return "—"
    if abs(x - round(x)) < 0.05:
        return str(int(round(x)))
    return f"{x:.1f}"


def fmt_ms(x) -> str:
    if x is None:
        return "—"
    return f"{x:.1f}"


def fmt_fail(x) -> str:
    if x is None:
        return "—"
    return f"{x * 100:.2f}%"


def truncate(s: str, n: int) -> str:
    s = " ".join(s.split())
    return s if len(s) <= n else s[: n - 1] + "…"


def batch_line(manifest: dict) -> str:
    b = manifest.get("orderer_batch") or {}
    mc = b.get("MaxMessageCount") or b.get("max_message_count") or 0
    to = b.get("BatchTimeout") or b.get("batch_timeout") or ""
    pref = b.get("PreferredMaxBytes") or b.get("preferred_max_bytes") or ""
    if not mc and not to:
        return "n/a"
    return f"msgcount={mc}  timeout={to}  preferred={pref}"


def crypto_line(manifest: dict) -> str:
    c = manifest.get("crypto") or {}
    sig = c.get("signature_alg") or "?"
    ha = c.get("hash_alg") or "?"
    per = c.get("per_tx_endorsement_verify")
    per_s = "per_tx_endorse_verify=true" if per else "per_tx_endorse_verify=false"
    return f"sig={sig}  hash={ha}  {per_s}"


def nodes_line(manifest: dict) -> str:
    nodes = manifest.get("nodes") or {}
    parts = [f"{k}×{v}" for k, v in nodes.items() if v]
    n = manifest.get("resource_containers") or 0
    cpu = manifest.get("resource_cpus_total") or 0
    mem = manifest.get("resource_memory_total_gb") or 0
    topo = " · ".join(parts) if parts else f"{n} containers"
    return f"{topo}   ·   {n} containers   ·   {cpu:g} CPU / {mem:g} GB"


def top_sweep_offered(phases) -> int:
    top = 0
    for ph in phases or []:
        if str(ph.get("name") or "").startswith("sweep-"):
            top = max(top, int(ph.get("offered_tps") or 0))
    return top


def primary_caveat(manifest: dict) -> str:
    caves = list(manifest.get("caveats") or [])
    prefer = (
        "oom-killed",
        "container failed",
        "did not saturate",
        "lower bound",
        "json-wrap",
        "json-wrapped",
        "parity not held",
        "epoch-10000",
        "kv-mixed",
    )
    skip = ("native metrics scrape", "informational metrics only")
    filtered = [c for c in caves if not any(s in c.lower() for s in skip)]
    for p in prefer:
        for c in filtered:
            if p in c.lower():
                return truncate(c, 220)
    return truncate(filtered[0], 220) if filtered else ""


# --- ingest ------------------------------------------------------------------

class Run:
    """One SUMMARY.tsv run-stage row plus resolved result.json / HTML."""

    def __init__(self, platform, config, status, reason, step_dir):
        self.platform = platform
        self.config = config
        self.status = status
        self.reason = reason or ""
        self.step_dir = step_dir
        self.result_dir: Path | None = None
        self.result: dict = {}
        self.html: Path | None = None
        self.summary_txt: Path | None = None

    @property
    def manifest(self) -> dict:
        return self.result.get("manifest") or {}

    @property
    def headline(self) -> dict | None:
        return self.result.get("headline")

    @property
    def phases(self) -> list:
        return self.result.get("phases") or []

    @property
    def normalized(self) -> bool:
        return bool(self.manifest.get("normalized"))

    def has_headline(self) -> bool:
        h = self.headline
        return bool(h) and int(h.get("committed") or 0) > 0

    def container_failed(self) -> bool:
        if self.status in ("container-failed", "run-failed", "deploy-failed"):
            return True
        if self.manifest.get("container_failures"):
            return True
        blob = (self.reason + " " + " ".join(self.manifest.get("caveats") or [])).lower()
        return "oom-killed" in blob or "oom killed" in blob

    def never_saturated(self) -> bool:
        sat = int(self.result.get("saturation_tps") or 0)
        top = top_sweep_offered(self.phases)
        skipped = self.manifest.get("skipped_steps") or []
        if top and sat == top and not skipped:
            return True
        text = " ".join(self.manifest.get("caveats") or []).lower()
        return "did not saturate" in text or "lower bound on its knee" in text

    def excluded(self) -> bool:
        if self.container_failed() or not self.has_headline():
            return True
        h = self.headline or {}
        if h.get("invariant_ok") is False:
            return True
        return False

    def kind(self) -> str:
        if self.excluded():
            return "excluded"
        if self.never_saturated():
            return "lower-bound"
        return "ok"

    def kind_label(self) -> str:
        return {"excluded": "excluded", "lower-bound": "lower bound", "ok": "hold"}[self.kind()]

    def hold_tps(self):
        if not self.has_headline():
            return None
        return self.headline.get("confirmed_tps")

    def e2e(self, which: str):
        if not self.has_headline():
            return None
        return pctl(self.headline.get("e2e_latency"), which)

    def fail_rate(self):
        if not self.has_headline():
            return None
        return self.headline.get("failure_rate")

    def knee(self):
        sat = self.result.get("saturation_tps")
        return int(sat) if sat else None

    def exclude_why(self) -> str:
        bits = []
        fails = self.manifest.get("container_failures") or []
        if fails:
            names = []
            for f in fails:
                if isinstance(f, dict):
                    names.append(f.get("name") or f.get("container") or str(f))
                else:
                    names.append(str(f))
            blob = " ".join(str(x) for x in fails).lower()
            if "oom" in blob or "137" in blob:
                bits.append("OOM: " + truncate(", ".join(names[:2]), 80))
            else:
                bits.append("container failed: " + truncate(", ".join(names[:2]), 80))
        elif self.container_failed():
            if "oom" in self.reason.lower() or "137" in self.reason:
                bits.append("OOM — platform container hit its memory cap")
            else:
                bits.append(truncate(self.reason, 140) or "container failed")
        if not self.has_headline():
            bits.append("no headline")
        if self.never_saturated() and self.has_headline():
            top = top_sweep_offered(self.phases) or self.knee() or 0
            bits.append(f"never saturated — {top} TPS is a lower bound, not a knee")
        h = self.headline or {}
        if h.get("invariant_ok") is False:
            bits.append("invariant broken")
        return "; ".join(bits) or truncate(self.reason, 160) or self.status


def _last_results_written(log_path: Path) -> str | None:
    if not log_path.is_file():
        return None
    found = None
    try:
        text = log_path.read_text(errors="replace")
    except OSError:
        return None
    text = re.sub(r"\x1b\[[0-9;]*m", "", text)
    for m in RESULTS_WRITTEN.finditer(text):
        found = m.group(1).rstrip(").,]")
    return found


def load_campaign(path: str | Path) -> tuple[Path, list[Run]]:
    campaign = Path(path).expanduser().resolve()
    if not campaign.is_dir():
        sys.exit(f"campaign directory not found: {campaign}")
    summary = campaign / "SUMMARY.tsv"
    if not summary.is_file():
        sys.exit(f"missing SUMMARY.tsv in {campaign}")

    root = repo_root()
    runs: list[Run] = []
    with summary.open(newline="") as f:
        reader = csv.DictReader(f, delimiter="\t")
        for row in reader:
            stage = (row.get("stage") or "").strip()
            if stage and stage != "run":
                continue
            platform = (row.get("platform") or "").strip()
            if not platform:
                continue
            r = Run(
                platform=platform,
                config=(row.get("config") or "").strip(),
                status=(row.get("status") or "").strip(),
                reason=(row.get("reason") or "").strip(),
                step_dir=(row.get("dir") or "").strip(),
            )
            step = campaign / r.step_dir if r.step_dir else campaign / platform / r.config
            rel = _last_results_written(step / "run.log")
            if not rel:
                # campaign-level log as a fallback (SUMMARY.reason truncates the path)
                rel = None
                for log in (campaign / "run-all.log", campaign / "gcp-run.log"):
                    if not log.is_file():
                        continue
                    text = re.sub(r"\x1b\[[0-9;]*m", "", log.read_text(errors="replace"))
                    hits = [m.group(1).rstrip(").,") for m in RESULTS_WRITTEN.finditer(text)]
                    # last write whose path contains this platform
                    for h in reversed(hits):
                        if f"/{platform}/" in f"/{h}/" or h.startswith(f"results/{platform}/"):
                            rel = h
                            break
                    if rel:
                        break
            if rel:
                rd = Path(rel)
                r.result_dir = rd if rd.is_absolute() else (root / rd)
            if r.result_dir and (r.result_dir / "result.json").is_file():
                try:
                    r.result = json.loads((r.result_dir / "result.json").read_text())
                except json.JSONDecodeError as e:
                    print(f"warning: {r.result_dir / 'result.json'}: {e}", file=sys.stderr)
            html_path = (r.result_dir / "monitoring-report.html") if r.result_dir else None
            if html_path and html_path.is_file():
                r.html = html_path
            st = (r.result_dir / "summary.txt") if r.result_dir else None
            if st and st.is_file():
                r.summary_txt = st
            runs.append(r)
    if not runs:
        sys.exit(f"no run-stage rows in {summary}")
    return campaign, runs


def extract_charts(html_path: Path) -> list[tuple[str, bytes]]:
    """Return (title, png-bytes) for charts that actually have an embedded PNG."""
    try:
        text = html_path.read_text(errors="replace")
    except OSError:
        return []
    out: list[tuple[str, bytes]] = []
    for m in CHART_BLOCK.finditer(text):
        title = html.unescape(re.sub(r"<[^>]+>", "", m.group(1))).strip()
        b64 = m.group(2)
        if not b64:
            continue
        raw = html.unescape(b64)
        try:
            data = base64.b64decode(raw, validate=False)
        except Exception:
            continue
        if len(data) < 64:
            continue
        out.append((title, data))
    return out


def pick_resource_charts(charts: list[tuple[str, bytes]]) -> list[tuple[str, bytes]]:
    def find(needles):
        for title, data in charts:
            t = title.lower()
            if any(n.lower() in t for n in needles):
                return title, data
        return None

    picked: list[tuple[str, bytes]] = []
    seen = set()
    for key, needles in CHART_PREFER:
        hit = find(needles)
        if hit and key not in seen:
            picked.append(hit)
            seen.add(key)
    if not picked:
        for key, needles in CHART_FALLBACK:
            hit = find(needles)
            if hit and key not in seen:
                picked.append(hit)
                seen.add(key)
    return picked[:3]


def render_ladder(run: Run) -> bytes | None:
    phases = run.phases
    if not phases:
        return None
    names, offered, confirmed, fail = [], [], [], []
    for ph in phases:
        names.append(str(ph.get("name") or ""))
        offered.append(float(ph.get("offered_tps") or 0))
        res = ph.get("result") or {}
        confirmed.append(float(res.get("confirmed_tps") or 0))
        fail.append(float(res.get("failure_rate") or 0) * 100.0)

    fig, ax = plt.subplots(figsize=(11.2, 4.4), dpi=140)
    fig.patch.set_facecolor("#1A1D23")
    ax.set_facecolor("#1A1D23")
    x = list(range(len(names)))
    ax.plot(x, offered, linestyle="--", marker="o", color="#8B96A4",
            linewidth=1.6, markersize=5, label="offered")
    ax.plot(x, confirmed, linestyle="-", marker="o", color="#3D8BFF",
            linewidth=2.2, markersize=6, label="confirmed (T3)")
    ax.set_ylabel("TPS", color="#E8ECF1")
    ax.set_xticks(x)
    labels = []
    for n, o in zip(names, offered):
        if n.startswith("sweep-"):
            labels.append(str(int(o)))
        else:
            labels.append(n)
    ax.set_xticklabels(labels, rotation=30, ha="right", color="#E8ECF1")
    ax.tick_params(colors="#E8ECF1")
    ax.spines["top"].set_visible(False)
    ax.spines["right"].set_visible(False)
    for sp in ax.spines.values():
        sp.set_color("#2A313C")
    ax.grid(True, axis="y", color="#2A313C", alpha=0.9)

    ax2 = ax.twinx()
    ax2.plot(x, fail, linestyle=":", marker="s", color="#E0A0A4",
             linewidth=1.4, markersize=4, label="fail %")
    ax2.set_ylabel("fail rate %", color="#E0A0A4")
    ax2.tick_params(colors="#E0A0A4")
    ax2.spines["top"].set_visible(False)
    ax2.spines["right"].set_color("#2A313C")
    ax2.set_ylim(bottom=0)

    h1, l1 = ax.get_legend_handles_labels()
    h2, l2 = ax2.get_legend_handles_labels()
    leg = ax.legend(h1 + h2, l1 + l2, loc="upper left", framealpha=0.9,
                    facecolor="#22262E", edgecolor="#2A313C", labelcolor="#E8ECF1", fontsize=8)
    for t in leg.get_texts():
        t.set_color("#E8ECF1")
    ax.set_xlabel("phase (sweep labels = offered TPS)", color="#8B96A4")
    fig.tight_layout()
    buf = io.BytesIO()
    fig.savefig(buf, format="png", dpi=140, facecolor=fig.get_facecolor())
    plt.close(fig)
    return buf.getvalue()


# --- tables / KPI ------------------------------------------------------------

def style_cell(cell, text, *, size=12, color=TEXT, bold=False, fill=None,
               align=PP_ALIGN.LEFT, name=FONT):
    cell.text = ""
    tf = cell.text_frame
    tf.word_wrap = True
    p = tf.paragraphs[0]
    p.alignment = align
    run = p.add_run()
    run.text = text
    run.font.size = Pt(size)
    run.font.color.rgb = color
    run.font.bold = bold
    run.font.name = name
    if fill is not None:
        rgb_fill(cell, fill)
    else:
        cell.fill.background()


def add_table(slide, left, top, width, rows, col_widths, *, header=True):
    n_rows, n_cols = len(rows), len(rows[0])
    table_shape = slide.shapes.add_table(n_rows, n_cols, left, top, width, Inches(0.36 * n_rows))
    table = table_shape.table
    for i, w in enumerate(col_widths):
        table.columns[i].width = w
    for r, row in enumerate(rows):
        is_header = header and r == 0
        kind = row[-1] if (not is_header and len(row) == n_cols) else ""
        # last logical column may be a style tag stored alongside — we only
        # style from an optional _kind on the Run, passed as extra in caller.
        fill = CARD
        color = TEXT
        if is_header:
            fill = RGBColor(0x15, 0x18, 0x1E)
            color = MUTED
        for c, val in enumerate(row):
            align = PP_ALIGN.RIGHT if c > 0 and not is_header else PP_ALIGN.LEFT
            if is_header:
                align = PP_ALIGN.LEFT if c == 0 else PP_ALIGN.RIGHT
            name = FONT_NUM if (c > 0 and not is_header) else FONT
            style_cell(table.cell(r, c), str(val), size=11 if not is_header else 10,
                       color=color, bold=is_header or c == 0, fill=fill,
                       align=align, name=name)
    return table


def kpi_card(slide, left, top, width, height, value, label, *, sub=None, muted=False):
    fill = AMBER_BG if muted else CARD
    card(slide, left, top, width, height, fill=fill)
    add_text(slide, left + Inches(0.12), top + Inches(0.12), width - Inches(0.2), Inches(0.7),
             value, size=32 if len(str(value)) > 8 else 44, color=AMBER if muted else ACCENT,
             bold=True, name=FONT_NUM, align=PP_ALIGN.LEFT, anchor=MSO_ANCHOR.MIDDLE)
    add_text(slide, left + Inches(0.12), top + height - Inches(0.55), width - Inches(0.2), Inches(0.28),
             label, size=12, color=MUTED, bold=False)
    if sub:
        add_text(slide, left + Inches(0.12), top + height - Inches(0.32), width - Inches(0.2), Inches(0.22),
                 sub, size=10, color=AMBER if muted else MUTED)


# --- explainer slides --------------------------------------------------------

def slide_title(prs, campaign_id, runs: list[Run], generated_at: str):
    slide = new_slide(prs)
    m = next((r.manifest for r in runs if r.manifest), {})
    profile = m.get("profile") or "—"
    workload = m.get("workload") or "—"
    run_name = m.get("run_name") or campaign_id
    flags = {bool(r.manifest.get("normalized")) for r in runs if r.manifest}
    native = flags == {False}
    add_text(slide, Inches(0.55), Inches(1.7), Inches(12.2), Inches(0.35),
             "CAMPAIGN REPORT", size=14, color=ACCENT, bold=True)
    add_text(slide, Inches(0.55), Inches(2.1), Inches(12.2), Inches(0.9),
             campaign_id, size=32, color=TEXT, bold=True, name=FONT_NUM)
    bits = [f"profile {profile}", f"workload {workload}", run_name]
    if native:
        bits.append("platform-native — not a ranking")
    elif flags == {True}:
        bits.append("normalized comparison set")
    add_text(slide, Inches(0.55), Inches(3.1), Inches(12.2), Inches(0.4),
             "  ·  ".join(bits), size=16, color=MUTED)
    add_text(slide, Inches(0.55), Inches(5.6), Inches(12.2), Inches(0.3),
             f"generated  {generated_at}", size=14, color=MUTED, name=FONT_NUM)
    add_text(slide, Inches(0.55), Inches(5.95), Inches(12.2), Inches(0.3),
             "Numbers from result.json  ·  charts from monitoring-report.html",
             size=13, color=MUTED)
    return slide


def slide_agenda(prs):
    slide = new_slide(prs)
    kicker_title(slide, "CONTENTS", "Agenda")
    items = [
        "1.  How to read a number — T1 / T2 / T3, confirmed vs offered",
        "2.  Probe-and-sweep — ladder, hold at 0.9×knee, in-flight cap",
        "3.  Load path — Gateway gRPC / Arma gRPC / ZMQ",
        "4.  Headline results and why some rows are excluded",
        "5.  Workload semantics — kv-mixed reads, JSON-wrap, state DB",
        "6.  Per-platform deep dive, then takeaways",
    ]
    add_paras(slide, Inches(0.7), Inches(1.5), Inches(11.5), Inches(5.0), items, size=20, spacing=14)
    return slide


def slide_how_to_read(prs):
    slide = new_slide(prs)
    kicker_title(slide, "SEMANTICS", "How to read a number",
                 subtitle="Headline throughput and latency always use T3. NeuChain T2 is a local send, not submit latency.")
    rows = [
        ["Stamp", "Meaning", "Who sets it"],
        ["T1", "Immediately before Submit", "Load generator"],
        ["T2", "Platform acknowledged receipt (async ack)", "Adapter Submit / AckTime"],
        ["T3", "Committed in a block, or read path returns", "Adapter WaitForFinality"],
    ]
    add_table(slide, Inches(0.55), Inches(1.45), Inches(12.2), rows,
              [Inches(1.4), Inches(6.6), Inches(4.2)])
    bullets = [
        "Headline TPS = confirmed commits per second from T3 timestamps, never T1.",
        "Offered TPS = submitted / wall-clock — the load actually presented.",
        "End-to-end latency = T3 − scheduled send (coordinated-omission safe).",
        "NeuChain T2 is “bytes left the client socket.” Never quote it as submit latency.",
        "A hold with every sweep step held is a lower bound on the knee, not the knee.",
    ]
    add_paras(slide, Inches(0.7), Inches(3.55), Inches(12.0), Inches(3.2), bullets, size=16, spacing=8)
    return slide


def slide_probe_sweep(prs):
    slide = new_slide(prs)
    kicker_title(slide, "METHODOLOGY", "Probe-and-sweep")
    items = [
        "Probe at 10 TPS × 30 s — floor latency, no load on the platform.",
        "Sweep a shared ladder (same steps on every platform). A step holds only if fail ≤ 2%, confirmed ≥ 95% of offered, and send-gap p99 ≤ 50 ms.",
        "After 2 consecutive failed steps, higher rungs are skipped. That is abort, not a shorter config.",
        "Hold 5 min at 0.9 × the highest step that actually held — not 90% of the top of the YAML.",
        "In-flight cap ≈ max(target_tps × 8, 1024). If the platform stops finalizing, excess txs fail as “not sent: already in flight.”",
        "Same seed, key space, warmup/cooldown, and sequential isolation. Native and normalized never share a ranking table.",
    ]
    add_paras(slide, Inches(0.7), Inches(1.4), Inches(12.0), Inches(5.4), items, size=17, spacing=10)
    return slide


def slide_load_path_table(prs):
    slide = new_slide(prs)
    kicker_title(slide, "LOAD PATH", "What hits the wire",
                 subtitle="One logical Transaction. Each adapter turns it into the platform’s real API.")
    rows = [
        ["", "fabric-cft / bft", "drunix", "fabricx", "neuchain"],
        ["Client API", "Gateway gRPC", "Gateway gRPC (LP)", "Arma + deliver gRPC", "ZMQ PUB/REQ"],
        ["Contract", "kvstore chaincode", "same kvstore", "adapter RW-set (ns 0)", "YCSB protobuf"],
        ["T2", "orderer/gateway accept", "LP/orderer accept", "first router SUCCESS", "local send return"],
        ["T3", "peer block events", "Committing Peer events", "deliver :4001", "tip/block poll"],
    ]
    add_table(slide, Inches(0.4), Inches(1.45), Inches(12.5), rows,
              [Inches(1.6), Inches(2.7), Inches(2.7), Inches(2.85), Inches(2.65)])
    add_text(slide, Inches(0.55), Inches(4.55), Inches(12.2), Inches(1.6),
             "NeuChain T2 is not a platform acknowledgement. Compare NeuChain on confirmed TPS and e2e (T3) only.\n"
             "Fabric-X T2 is a real ack: first SUCCESS from the four Arma routers, before ordering and commit.",
             size=15, color=MUTED)
    return slide


def slide_load_path_fabric_family(prs):
    slide = new_slide(prs)
    kicker_title(slide, "LOAD PATH", "Fabric family — Gateway gRPC + chaincode")
    labels = ["Tx", "adapter", "Gateway\n:7051", "kvstore CC", "Orderer", "VSCC / MVCC", "T3"]
    left, top = Inches(0.4), Inches(1.55)
    bw, bh, gap = Inches(1.55), Inches(0.85), Inches(0.28)
    x = left
    for i, lab in enumerate(labels):
        flow_box(slide, x, top, bw, bh, lab, accent=(i == len(labels) - 1))
        x += bw
        if i < len(labels) - 1:
            arrow(slide, x + Inches(0.04), top + Inches(0.34), gap - Inches(0.08))
            x += gap
    # three variant cards
    variants = [
        ("fabric-cft", "Raft ×1 orderer\nLevelDB  ·  5 containers\nEndorse → order → commit"),
        ("fabric-bft", "SmartBFT ×4 orderers\nSame Put path as CFT\nThinner per-container RAM"),
        ("drunix", "Lite Peer → CP :7061\nYugabyte  ·  JSON-wrap\n~13 containers on this profile"),
    ]
    vw, vh = Inches(3.85), Inches(2.35)
    for i, (title, body) in enumerate(variants):
        x = Inches(0.55) + i * (vw + Inches(0.25))
        y = Inches(2.75)
        card(slide, x, y, vw, vh)
        add_text(slide, x + Inches(0.18), y + Inches(0.14), vw - Inches(0.3), Inches(0.35),
                 title, size=16, color=ACCENT, bold=True)
        add_text(slide, x + Inches(0.18), y + Inches(0.55), vw - Inches(0.3), Inches(1.6),
                 body, size=14, color=TEXT)
    add_text(slide, Inches(0.55), Inches(5.3), Inches(12.2), Inches(1.4),
             "kv-read / kv-mixed reads on this family are Gateway Evaluate on one peer — no orderer, no commit.\n"
             "That is not the same path as a NeuChain read, which is a committed transaction.",
             size=14, color=MUTED)
    return slide


def slide_load_path_fx_nc(prs):
    slide = new_slide(prs)
    kicker_title(slide, "LOAD PATH", "Fabric-X (Arma gRPC)  and  NeuChain (ZMQ)")
    # left column Fabric-X
    card(slide, Inches(0.45), Inches(1.4), Inches(6.15), Inches(5.4))
    add_text(slide, Inches(0.65), Inches(1.55), Inches(5.8), Inches(0.35),
             "fabricx — no chaincode", size=16, color=ACCENT, bold=True)
    fx = ["TxWrite", "sign ns 0", "4 Arma\nrouters", "T2 first\nSUCCESS", "committer\n+ Postgres", "deliver\n:4001  T3"]
    y = Inches(2.1)
    bw, bh = Inches(1.7), Inches(0.7)
    # two rows of 3
    for row_i in range(2):
        x = Inches(0.7)
        for col_i in range(3):
            idx = row_i * 3 + col_i
            flow_box(slide, x, y, bw, bh, fx[idx], accent=(idx == 5))
            if col_i < 2:
                arrow(slide, x + bw + Inches(0.04), y + Inches(0.26), Inches(0.22))
            x += bw + Inches(0.3)
        y += Inches(0.95)
    add_text(slide, Inches(0.7), Inches(4.2), Inches(5.6), Inches(2.2),
             "Broadcast the same envelope to all four routers (avoids ~10 s FirstStrike).\n"
             "Reads are QueryService GetRows — not ordered.\n"
             "T2 is a real platform ack.",
             size=13, color=MUTED)

    card(slide, Inches(6.75), Inches(1.4), Inches(6.15), Inches(5.4))
    add_text(slide, Inches(6.95), Inches(1.55), Inches(5.8), Inches(0.35),
             "neuchain — EV, no ordering", size=16, color=ACCENT, bold=True)
    nc = ["YCSB +\nRSA-1024", "ZMQ PUB\n:5001", "T2 local\nsend", "EV nodes", "poll tip\n:7003", "T3"]
    y = Inches(2.1)
    for row_i in range(2):
        x = Inches(7.0)
        for col_i in range(3):
            idx = row_i * 3 + col_i
            flow_box(slide, x, y, bw, bh, nc[idx], accent=(idx == 5))
            if col_i < 2:
                arrow(slide, x + bw + Inches(0.04), y + Inches(0.26), Inches(0.22))
            x += bw + Inches(0.3)
        y += Inches(0.95)
    add_text(slide, Inches(7.0), Inches(4.2), Inches(5.6), Inches(2.2),
             "T2 is not comparable — it is the local send return, not a platform ack.\n"
             "Never quote NeuChain submit latency.\n"
             "Reads are committed transactions, unlike Fabric Evaluate.",
             size=13, color=MUTED)
    return slide


def _campaign_mode_label(runs: list[Run]) -> str:
    flags = {r.normalized for r in runs if r.manifest}
    if flags == {False}:
        return "platform-native — not a ranking"
    if flags == {True}:
        return "normalized — comparable within this table only"
    return "mixed native/normalized — shown in separate tables, never ranked together"


def _headline_groups(runs: list[Run]) -> list[tuple[str, list[Run]]]:
    """Native and normalized never share a ranking table."""
    native = [r for r in runs if r.manifest and not r.normalized]
    norm = [r for r in runs if r.manifest and r.normalized]
    unknown = [r for r in runs if not r.manifest]
    groups: list[tuple[str, list[Run]]] = []
    if native:
        extra = unknown if not norm else []
        groups.append(("platform-native — not a ranking", native + extra))
        if extra:
            unknown = []
    if norm:
        groups.append(("normalized — not mixed with native", norm + unknown))
        unknown = []
    if not groups:
        groups.append((_campaign_mode_label(runs), runs))
    elif unknown:
        groups[0] = (groups[0][0], groups[0][1] + unknown)
    return groups


def _table_fill_for_kind(kind: str) -> RGBColor:
    if kind == "excluded":
        return AMBER_BG
    if kind == "lower-bound":
        return BOUND_BG
    return CARD


def slide_headline(prs, runs: list[Run]):
    groups = _headline_groups(runs)
    # Never put native and normalized on one ranking table: one slide per group.
    slides = []
    for label, group in groups:
        slide = new_slide(prs)
        kicker_title(slide, "RESULTS", "Headline results", subtitle=label)
        # KPI cards: included runs only (hold or lower-bound), campaign order, not sorted by TPS.
        shown = [r for r in group if r.has_headline()]
        n = max(len(shown), 1)
        gap = Inches(0.18)
        usable = Inches(12.2)
        cw = (usable - gap * (min(n, 4) - 1)) / min(n, 4) if shown else usable
        for i, r in enumerate(shown[:4]):
            x = Inches(0.55) + i * (cw + gap)
            muted = r.kind() == "excluded"
            sub = r.kind_label() if muted else f"p50 {fmt_ms(r.e2e('p50'))} ms"
            kpi_card(slide, x, Inches(1.4), cw, Inches(1.55),
                     fmt_tps(r.hold_tps()), r.platform, sub=sub, muted=muted)

        header = ["Platform", "Status", "Hold TPS", "p50 ms", "p99 ms", "Fail", "Knee"]
        body = [header]
        for r in group:
            knee = r.knee()
            if r.never_saturated() and knee:
                knee_s = f"≥ {knee}"
            elif knee:
                knee_s = str(knee)
            else:
                knee_s = "—"
            body.append([
                r.platform,
                r.kind_label(),
                fmt_tps(r.hold_tps()),
                fmt_ms(r.e2e("p50")),
                fmt_ms(r.e2e("p99")),
                fmt_fail(r.fail_rate()),
                knee_s,
            ])
        widths = [Inches(2.0), Inches(1.7), Inches(1.6), Inches(1.5), Inches(1.5), Inches(1.4), Inches(1.6)]
        n_rows, n_cols = len(body), 7
        top = Inches(3.2)
        table_shape = slide.shapes.add_table(n_rows, n_cols, Inches(0.55), top, Inches(12.2),
                                             Inches(0.38 * n_rows))
        table = table_shape.table
        for i, w in enumerate(widths):
            table.columns[i].width = w
        for ri, row in enumerate(body):
            is_header = ri == 0
            kind = group[ri - 1].kind() if not is_header else "ok"
            fill = RGBColor(0x15, 0x18, 0x1E) if is_header else _table_fill_for_kind(kind)
            color = MUTED if is_header else (AMBER if kind == "excluded" else TEXT)
            for ci, val in enumerate(row):
                align = PP_ALIGN.LEFT if ci == 0 else PP_ALIGN.RIGHT
                if ci == 1:
                    align = PP_ALIGN.LEFT
                name = FONT if (is_header or ci <= 1) else FONT_NUM
                style_cell(table.cell(ri, ci), str(val), size=11, color=color,
                           bold=is_header or ci == 0, fill=fill, align=align, name=name)
        add_text(slide, Inches(0.55), Inches(6.55), Inches(12.2), Inches(0.4),
                 "Amber rows are excluded (OOM / no headline). Blue-grey rows did not saturate — quote as a lower bound, not a knee. Not sorted by TPS.",
                 size=12, color=MUTED)
        slides.append(slide)
    return slides


def slide_exclusions(prs, runs: list[Run]):
    slide = new_slide(prs)
    kicker_title(slide, "RESULTS", "Why rows are excluded — or not a knee")
    lines = []
    for r in runs:
        why = r.exclude_why()
        if r.kind() == "ok" and not why:
            continue
        if r.kind() == "ok":
            continue
        tag = "EXCLUDED" if r.kind() == "excluded" else "LOWER BOUND"
        lines.append(f"{r.platform}  ·  {tag}  ·  {why}")
    if not lines:
        lines = ["No excluded rows in this campaign. Unsaturated ladders still are not knees."]
    add_paras(slide, Inches(0.7), Inches(1.45), Inches(12.0), Inches(4.4), lines, size=16, spacing=12)
    add_text(slide, Inches(0.55), Inches(6.1), Inches(12.2), Inches(0.7),
             "OOM is a resource-split failure, not evidence the platform “cannot do TPS.” "
             "A lower bound means extend the ladder; do not publish it as saturation.",
             size=14, color=MUTED)
    return slide


def slide_semantics(prs, runs: list[Run]):
    slide = new_slide(prs)
    kicker_title(slide, "WORKLOAD", "Read / write semantics")
    items = [
        "kv-mixed at 50% reads: headline TPS blends two paths and is not a write ceiling.",
        "Fabric / Drunix / Fabric-X reads are Evaluate / QueryService — not ordered, not committed.",
        "NeuChain reads are committed transactions. Do not compare mixed-read TPS across those families as if the work were equal.",
        "Drunix write values are JSON-wrapped client-side so Yugabyte JSONB does not panic; on-wire size differs.",
        "state_db is what actually ran; state_db_requested is the contract. They differ on Drunix (Yugabyte) and Fabric-X (Postgres).",
        "Native and normalized numbers never share a ranking table. Native kv-mixed is not ranked against native kv-write.",
    ]
    add_paras(slide, Inches(0.7), Inches(1.4), Inches(12.0), Inches(4.6), items, size=16, spacing=10)
    # requested vs actual from this campaign
    bits = []
    for r in runs:
        m = r.manifest
        if not m:
            continue
        req, got = m.get("state_db_requested") or "?", m.get("state_db") or "?"
        mark = "parity held" if req == got else "PARITY NOT HELD"
        bits.append(f"{r.platform}: {got} (requested {req}, {mark})")
    if bits:
        add_text(slide, Inches(0.55), Inches(6.15), Inches(12.2), Inches(0.7),
                 "  ·  ".join(bits), size=11, color=MUTED)
    return slide


def slide_platform_kpi(prs, run: Run):
    slide = new_slide(prs)
    m = run.manifest
    ver = m.get("platform_version") or ""
    kicker_title(slide, run.platform.upper(), run.platform,
                 subtitle=(ver + ("  ·  platform-native" if m and not run.normalized else "")).strip(" ·"))
    kind = run.kind()
    cards = []
    if run.has_headline() and run.platform != "neuchain":
        cards = [
            (fmt_tps(run.hold_tps()), "confirmed TPS (T3)"),
            (fmt_ms(run.e2e("p50")), "e2e p50 ms"),
            (fmt_ms(run.e2e("p99")), "e2e p99 ms"),
            (fmt_fail(run.fail_rate()), "fail rate"),
        ]
    elif run.has_headline():
        # NeuChain: never quote T2 / submit latency.
        cards = [
            (fmt_tps(run.hold_tps()), "confirmed TPS (T3)"),
            (fmt_ms(run.e2e("p50")), "e2e p50 ms"),
            (fmt_ms(run.e2e("p99")), "e2e p99 ms"),
            (fmt_fail(run.fail_rate()), "fail rate"),
        ]
    else:
        cards = [("—", "no headline"), (run.status or "—", "campaign status"),
                 (fmt_tps(run.knee()), "detected saturation"), ("excluded", "not a measurement")]

    cw = Inches(2.9)
    for i, (val, lab) in enumerate(cards[:4]):
        kpi_card(slide, Inches(0.55) + i * (cw + Inches(0.18)), Inches(1.4), cw, Inches(1.5),
                 val, lab, muted=(kind == "excluded"))

    y = Inches(3.15)
    card(slide, Inches(0.55), y, Inches(12.2), Inches(2.55))
    add_text(slide, Inches(0.75), y + Inches(0.12), Inches(11.8), Inches(0.3),
             "Topology  ·  batch  ·  crypto", size=12, color=ACCENT, bold=True)
    lines = [
        nodes_line(m) if m else "topology unknown — result.json missing",
        f"batch    {batch_line(m)}" if m else "",
        f"crypto   {crypto_line(m)}" if m else "",
        f"state DB {m.get('state_db') or '—'}   (requested {m.get('state_db_requested') or '—'})" if m else "",
        f"workload {m.get('workload') or '—'}   generators {m.get('generators') or '—'}   seed {m.get('seed') or '—'}" if m else "",
    ]
    add_text(slide, Inches(0.75), y + Inches(0.48), Inches(11.8), Inches(1.9),
             "\n".join(t for t in lines if t), size=14, color=TEXT, name=FONT_NUM)

    if kind == "excluded":
        caveat = run.exclude_why()
    else:
        caveat = primary_caveat(m) if m else ""
    if caveat:
        add_text(slide, Inches(0.55), Inches(5.85), Inches(12.2), Inches(0.95),
                 caveat, size=14, color=AMBER if kind == "excluded" else MUTED)
    if run.platform == "neuchain":
        add_text(slide, Inches(0.55), Inches(6.55), Inches(12.2), Inches(0.35),
                 "T2 is local send — submit/commit split is not shown and is not comparable.",
                 size=12, color=ACCENT)
    return slide


def slide_platform_ladder(prs, run: Run):
    slide = new_slide(prs)
    kicker_title(slide, run.platform.upper(), "Sweep ladder",
                 subtitle="Offered vs confirmed (T3) vs fail rate. Full phase table lives in summary.txt, not here.")
    png = render_ladder(run)
    if png:
        slide.shapes.add_picture(io.BytesIO(png), Inches(0.45), Inches(1.35),
                                 width=Inches(12.4), height=Inches(5.0))
    else:
        add_text(slide, Inches(0.7), Inches(3.0), Inches(12.0), Inches(1.0),
                 "No phase table in result.json — nothing to plot.",
                 size=18, color=MUTED)
    return slide


def slide_platform_resources(prs, run: Run):
    slide = new_slide(prs)
    kicker_title(slide, run.platform.upper(), "Host and container resources",
                 subtitle="PNG charts already embedded in monitoring-report.html. Empty placeholders skipped.")
    charts: list[tuple[str, bytes]] = []
    if run.html:
        charts = pick_resource_charts(extract_charts(run.html))
    if not charts:
        add_text(slide, Inches(0.7), Inches(3.0), Inches(12.0), Inches(1.2),
                 "No host/container CPU or memory PNG in this run’s monitoring report.",
                 size=18, color=MUTED)
        return slide
    n = len(charts)
    gap = Inches(0.2)
    usable = Inches(12.2)
    cw = (usable - gap * (n - 1)) / n
    ch = Inches(4.4)
    for i, (title, data) in enumerate(charts):
        x = Inches(0.55) + i * (cw + gap)
        card(slide, x, Inches(1.35), cw, ch + Inches(0.45))
        add_text(slide, x + Inches(0.1), Inches(1.4), cw - Inches(0.2), Inches(0.35),
                 title, size=11, color=MUTED)
        try:
            slide.shapes.add_picture(io.BytesIO(data), x + Inches(0.1), Inches(1.8),
                                     width=cw - Inches(0.2), height=ch - Inches(0.15))
        except Exception as e:
            add_text(slide, x + Inches(0.1), Inches(3.0), cw - Inches(0.2), Inches(1.0),
                     f"chart embed failed: {e}", size=12, color=AMBER)
    return slide


def slide_takeaways(prs, runs: list[Run]):
    slide = new_slide(prs)
    kicker_title(slide, "CLOSE", "Takeaways")
    native = all((not r.normalized) for r in runs if r.manifest)
    excluded = [r.platform for r in runs if r.kind() == "excluded"]
    lower = [r.platform for r in runs if r.kind() == "lower-bound"]
    held = [r.platform for r in runs if r.kind() == "ok"]
    lines = []
    if native:
        lines.append("This campaign is platform-native kv-mixed. These numbers are not a ranking, and they are not comparable to normalized kv-write or to native write ceilings.")
    else:
        lines.append("Quote only within this campaign’s normalized group. Do not mix native and normalized in one table.")
    if held:
        bits = [f"{r.platform} {fmt_tps(r.hold_tps())}" for r in runs if r.kind() == "ok"]
        lines.append("Real hold (a knee was found): " + ", ".join(bits) + ".")
    if lower:
        bits = [f"{r.platform} {fmt_tps(r.hold_tps())} (ladder ≥ {r.knee() or top_sweep_offered(r.phases)})" for r in runs if r.kind() == "lower-bound"]
        lines.append("Lower bound, not a knee — extend the ladder before quoting saturation: " + "; ".join(bits) + ".")
    if excluded:
        lines.append("Excluded (OOM / no headline): " + ", ".join(excluded) + ". Rerun on a profile that leaves more RAM per container; do not read this as a TPS ceiling.")
    lines.append("NeuChain T2 is never submit latency. kv-mixed headline TPS blends Evaluate/QueryService reads with committed NeuChain reads.")
    # cap at 5
    add_paras(slide, Inches(0.7), Inches(1.4), Inches(12.0), Inches(5.2), lines[:5], size=16, spacing=12)
    return slide


# --- main --------------------------------------------------------------------

def build(campaign: Path, runs: list[Run], out: Path) -> int:
    generated_at = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    campaign_id = campaign.name
    prs = Presentation()
    prs.slide_width = W
    prs.slide_height = H

    slide_title(prs, campaign_id, runs, generated_at)
    slide_agenda(prs)
    slide_how_to_read(prs)
    slide_probe_sweep(prs)
    slide_load_path_table(prs)
    slide_load_path_fabric_family(prs)
    slide_load_path_fx_nc(prs)
    slide_headline(prs, runs)
    slide_exclusions(prs, runs)
    slide_semantics(prs, runs)
    for run in runs:
        slide_platform_kpi(prs, run)
        slide_platform_ladder(prs, run)
        slide_platform_resources(prs, run)
    slide_takeaways(prs, runs)

    finalize_footers(prs, campaign_id)
    out.parent.mkdir(parents=True, exist_ok=True)
    prs.save(str(out))
    return len(prs.slides)


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--campaign", required=True, help="campaign directory (contains SUMMARY.tsv)")
    p.add_argument("--out", default=None, help="output pptx (default: <campaign>/deck.pptx)")
    args = p.parse_args(argv)

    campaign, runs = load_campaign(args.campaign)
    out = Path(args.out).expanduser().resolve() if args.out else campaign / "deck.pptx"
    n = build(campaign, runs, out)
    print(f"wrote {out}  ({n} slides, {len(runs)} platforms)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
