#!/usr/bin/env python3
"""Render a batch of time-series charts to base64 PNGs for the monitoring report.

    python3 scripts/render_run_report.py --render-charts < charts.json

Reads {"charts": [{"id", "title", "unit", "series": [{"legend", "times", "values"}]}]}
as JSON on stdin, writes {"<chart-id>": "data:image/png;base64,..."} as JSON on
stdout. Pure rendering: no project-schema knowledge (manifest/result JSON) lives
here, that stays in Go. Uses only matplotlib + stdlib, same as scripts/plot.py.
"""
import base64
import datetime
import io
import json
import sys

try:
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    import matplotlib.dates as mdates
except ImportError:
    sys.exit("pip install matplotlib")


def scale_for_unit(unit):
    # Returns (divisor, y-label suffix). Values are converted before plotting
    # so the y-axis reads in a human-scale unit instead of raw Prometheus units.
    if unit == "bytes":
        return 1024 * 1024, "MiB"
    if unit == "percentunit":
        return 0.01, "%"
    if unit == "Bps":
        return 1024 * 1024, "MB/s"
    if unit == "s":
        return 0.001, "ms"
    if unit == "percent":
        return 1, "%"
    return 1, ""


def render_chart(spec):
    unit = spec.get("unit", "")
    divisor, ylabel = scale_for_unit(unit)

    fig, ax = plt.subplots(figsize=(9, 4))
    any_points = False
    for s in spec.get("series", []):
        times = s.get("times", [])
        values = s.get("values", [])
        if not times:
            continue
        any_points = True
        dt = [datetime.datetime.fromtimestamp(t, tz=datetime.timezone.utc) for t in times]
        scaled = [v / divisor for v in values]
        if unit == "bool":
            ax.step(dt, scaled, where="post", label=s.get("legend") or "series")
        else:
            ax.plot(dt, scaled, label=s.get("legend") or "series")

    if not any_points:
        ax.text(0.5, 0.5, "no data in this window", ha="center", va="center", transform=ax.transAxes, color="#888")
        ax.set_xticks([])
        ax.set_yticks([])
    else:
        ax.set_ylabel(ylabel or "value")
        ax.xaxis.set_major_formatter(mdates.DateFormatter("%H:%M:%S"))
        fig.autofmt_xdate()
        if len(spec.get("series", [])) > 1:
            ax.legend(fontsize=8, loc="upper left")
        ax.grid(True, alpha=0.3)

    ax.set_title(spec.get("title", ""))
    fig.tight_layout()

    buf = io.BytesIO()
    fig.savefig(buf, format="png", dpi=110)
    plt.close(fig)
    return "data:image/png;base64," + base64.b64encode(buf.getvalue()).decode("ascii")


def main(argv):
    if "--render-charts" not in argv:
        sys.exit(__doc__)
    req = json.load(sys.stdin)
    out = {}
    for spec in req.get("charts", []):
        cid = spec.get("id")
        if not cid:
            continue
        try:
            out[cid] = render_chart(spec)
        except Exception as e:  # one bad chart must not blank the whole report
            print(f"warning: chart {cid!r} failed to render: {e}", file=sys.stderr)
    json.dump(out, sys.stdout)


if __name__ == "__main__":
    main(sys.argv[1:])
