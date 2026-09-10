#!/usr/bin/env python3
"""Plot throughput-latency curves from a probe-sweep run's phases.csv.

    scripts/plot.py results/fabric-cft/20260910-133207/phases.csv [more.csv ...]

Writes <dir>/curve.png next to each input, and if several inputs are given a
combined overlay at ./docs/reports/curve-overlay.png. Uses only matplotlib +
stdlib csv so it runs without pandas.
"""
import csv
import os
import sys

try:
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
except ImportError:
    sys.exit("pip install matplotlib")


def load(path):
    rows = []
    with open(path) as f:
        for r in csv.DictReader(f):
            if not r["phase"].startswith("sweep-"):
                continue
            rows.append({
                "offered": float(r["offered_tps"]),
                "confirmed": float(r["confirmed_tps"]),
                "p50": float(r["e2e_p50_ms"]),
                "p99": float(r["e2e_p99_ms"]),
                "fail": float(r["fail_rate"]),
            })
    rows.sort(key=lambda x: x["offered"])
    return rows


def plot_one(ax_tps, ax_lat, rows, label):
    x = [r["offered"] for r in rows]
    ax_tps.plot(x, [r["confirmed"] for r in rows], "o-", label=f"{label} confirmed TPS")
    ax_lat.plot(x, [r["p50"] for r in rows], "s--", label=f"{label} p50 ms")
    ax_lat.plot(x, [r["p99"] for r in rows], "^:", label=f"{label} p99 ms")


def figure():
    fig, ax_tps = plt.subplots(figsize=(8, 5))
    ax_lat = ax_tps.twinx()
    ax_tps.set_xlabel("offered load (TPS)")
    ax_tps.set_ylabel("confirmed throughput (TPS)")
    ax_lat.set_ylabel("end-to-end latency (ms)")
    ax_tps.grid(True, alpha=0.3)
    return fig, ax_tps, ax_lat


def finish(fig, ax_tps, ax_lat, out, title):
    lines = ax_tps.get_lines() + ax_lat.get_lines()
    ax_tps.legend(lines, [l.get_label() for l in lines], loc="upper left", fontsize=8)
    ax_tps.set_title(title)
    fig.tight_layout()
    fig.savefig(out, dpi=120)
    print("wrote", out)


def main(argv):
    if not argv:
        sys.exit(__doc__)
    fig_all, tps_all, lat_all = figure()
    for path in argv:
        rows = load(path)
        if not rows:
            print("no sweep rows in", path)
            continue
        label = os.path.basename(os.path.dirname(os.path.dirname(path))) or path
        fig, tps, lat = figure()
        plot_one(tps, lat, rows, label)
        finish(fig, tps, lat, os.path.join(os.path.dirname(path), "curve.png"),
               f"throughput-latency: {label}")
        plt.close(fig)
        plot_one(tps_all, lat_all, rows, label)
    if len(argv) > 1:
        os.makedirs("docs/reports", exist_ok=True)
        finish(fig_all, tps_all, lat_all, "docs/reports/curve-overlay.png",
               "throughput-latency overlay")


if __name__ == "__main__":
    main(sys.argv[1:])
