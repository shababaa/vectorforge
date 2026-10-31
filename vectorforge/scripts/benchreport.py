"""Render benchmark JSON into markdown tables and SVG charts.

  python vectorforge/scripts/benchreport.py

Reads vectorforge/results/bench/*.json and vectorforge/results/ml/*.json and
writes docs/vectorforge/benchmark-tables.md plus docs/vectorforge/img/*.svg.
Nothing is typed in by hand: every number in those files comes from the JSON.
Standard library only.
"""

from __future__ import annotations

import json
import math
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BENCH = ROOT / "vectorforge/results/bench"
ML = ROOT / "vectorforge/results/ml"
DOCS = ROOT / "docs/vectorforge"
IMG = DOCS / "img"
COLORS = ["#2563eb", "#dc2626", "#16a34a", "#9333ea", "#ea580c", "#0891b2", "#4b5563", "#ca8a04"]


def load(path: Path):
    return json.loads(path.read_text()) if path.exists() else None


# ---------- formatting ----------

def us(v: float) -> str:
    if v >= 1000:
        return f"{v / 1000:.2f} ms" if v < 100000 else f"{v / 1000:.0f} ms"
    return f"{v:.0f} µs"


def num(v: float, digits: int = 0) -> str:
    return f"{v:,.{digits}f}"


def mb(b: float) -> str:
    return f"{b / 1e6:.1f} MB"


def table(headers: list[str], rows: list[list]) -> str:
    out = ["| " + " | ".join(headers) + " |", "|" + "|".join("---" for _ in headers) + "|"]
    out += ["| " + " | ".join(str(c) for c in r) + " |" for r in rows]
    return "\n".join(out) + "\n"


# ---------- charts ----------

def _nice(lo: float, hi: float, n: int = 5) -> list[float]:
    span = hi - lo or abs(hi) or 1.0
    step = 10 ** math.floor(math.log10(span / n))
    for m in (1, 2, 2.5, 5, 10):
        if span / (step * m) <= n:
            step *= m
            break
    first = math.ceil(lo / step) * step
    return [round(first + i * step, 10) for i in range(int((hi - first) / step + 1e-9) + 1)]


def _log_ticks(lo: float, hi: float) -> list[float]:
    ticks = []
    for e in range(math.floor(math.log10(lo)), math.ceil(math.log10(hi)) + 1):
        for m in (1, 2, 5):
            v = m * 10 ** e
            if lo <= v <= hi:
                ticks.append(v)
    return ticks


def _tick_label(v: float) -> str:
    if v >= 1e6:
        return f"{v / 1e6:g}M"
    if v >= 1e3:
        return f"{v / 1e3:g}k"
    return f"{v:g}"


def line_chart(path: Path, title: str, series: list[tuple[str, list[tuple[float, float]]]], xlabel: str,
               ylabel: str, log_x: bool = False, y_range: tuple[float, float] | None = None) -> None:
    W, H, L, R, T, B = 720, 420, 64, 170, 36, 52
    pts = [p for _, s in series for p in s]
    if not pts:
        return
    xs, ys = [p[0] for p in pts], [p[1] for p in pts]
    x0, x1 = min(xs), max(xs)
    if log_x:
        x0, x1 = 10 ** math.floor(math.log10(x0)), 10 ** math.ceil(math.log10(x1))
    y0, y1 = y_range or (min(ys), max(ys))
    if y_range is None:
        pad = (y1 - y0) * 0.05 or 0.05
        y0, y1 = y0 - pad, y1 + pad

    def X(v):
        f = (math.log10(v) - math.log10(x0)) / (math.log10(x1) - math.log10(x0)) if log_x else (v - x0) / ((x1 - x0) or 1)
        return L + f * (W - L - R)

    def Y(v):
        return T + (1 - (v - y0) / ((y1 - y0) or 1)) * (H - T - B)

    o = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
         f'font-family="system-ui,Segoe UI,sans-serif" font-size="12">',
         f'<rect width="{W}" height="{H}" fill="#fff"/>',
         f'<text x="{L}" y="22" font-size="14" font-weight="600">{title}</text>']
    for t in (_log_ticks(x0, x1) if log_x else _nice(x0, x1)):
        o.append(f'<line x1="{X(t):.1f}" x2="{X(t):.1f}" y1="{T}" y2="{H - B}" stroke="#e5e7eb"/>')
        o.append(f'<text x="{X(t):.1f}" y="{H - B + 16}" text-anchor="middle" fill="#374151">{_tick_label(t)}</text>')
    for t in _nice(y0, y1):
        if y0 <= t <= y1:
            o.append(f'<line x1="{L}" x2="{W - R}" y1="{Y(t):.1f}" y2="{Y(t):.1f}" stroke="#e5e7eb"/>')
            o.append(f'<text x="{L - 6}" y="{Y(t) + 4:.1f}" text-anchor="end" fill="#374151">{t:g}</text>')
    o.append(f'<rect x="{L}" y="{T}" width="{W - L - R}" height="{H - T - B}" fill="none" stroke="#9ca3af"/>')
    o.append(f'<text x="{(L + W - R) / 2}" y="{H - 12}" text-anchor="middle">{xlabel}</text>')
    o.append(f'<text transform="translate(16 {(T + H - B) / 2}) rotate(-90)" text-anchor="middle">{ylabel}</text>')
    for i, (label, s) in enumerate(series):
        c = COLORS[i % len(COLORS)]
        s = sorted(s)
        if len(s) > 1:
            o.append(f'<polyline fill="none" stroke="{c}" stroke-width="2" points="'
                     + " ".join(f"{X(x):.1f},{Y(y):.1f}" for x, y in s) + '"/>')
        for x, y in s:
            o.append(f'<circle cx="{X(x):.1f}" cy="{Y(y):.1f}" r="3" fill="{c}"><title>{label}: {x:g}, {y:.4g}</title></circle>')
        ly = T + 8 + i * 18
        o.append(f'<line x1="{W - R + 12}" x2="{W - R + 30}" y1="{ly}" y2="{ly}" stroke="{c}" stroke-width="2"/>')
        o.append(f'<text x="{W - R + 36}" y="{ly + 4}">{label}</text>')
    o.append("</svg>")
    IMG.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(o) + "\n", encoding="utf-8")


# ---------- sections ----------

def env_line(rep: dict) -> str:
    e = rep["environment"]
    return (f"`{rep['suite']}` suite — {e['cpu'] or e['arch']}, {e['go_version']}, GOMAXPROCS={e['gomaxprocs']}, "
            f"commit `{e['git_commit']}`{' (dirty)' if e['git_dirty'] else ''}, timer {e['timer']}, "
            f"wall {rep['wall_seconds'] / 60:.1f} min, started {rep['started_at'][:19]}Z\n")


def best_points(ds: dict, targets=(0.90, 0.95, 0.99)) -> list[list]:
    rows = []
    exact = ds["exact"]["latency"]["p50_us"]
    for t in targets:
        ok = [r for r in ds["ivf"] if r["recall_at_10"] >= t]
        if not ok:
            rows.append([f"≥ {t:.2f}", "not reached", "", "", "", "", ""])
            continue
        r = min(ok, key=lambda r: r["latency"]["p50_us"])
        rows.append([f"≥ {t:.2f}", f"{r['nlist']} / {r['nprobe']}", f"{r['recall_at_10']:.3f}", us(r["latency"]["p50_us"]),
                     us(r["latency"]["p99_us"]), f"{100 * r['fraction_of_vectors_scored']:.1f} %",
                     f"{exact / r['latency']['p50_us']:.1f}×"])
    return rows


def dataset_section(ds: dict, chart_prefix: str, out: list[str], full: bool = True) -> None:
    d = ds["dataset"]
    name = d["name"]
    out.append(f"### {name}\n")
    desc = f"{num(d['n'])} vectors × {d['dim']} dims, {d['queries']} queries, kind `{d['kind']}`"
    if d.get("source"):
        desc += f", from `{d['source']}`"
    out.append(desc + ".\n")
    ex = ds["exact"]
    out.append(table(["exact search", "p50", "p95", "p99", "QPS (1 goroutine)", "index memory (est.)"],
                     [["flat scan", us(ex["latency"]["p50_us"]), us(ex["latency"]["p95_us"]), us(ex["latency"]["p99_us"]),
                       num(ex["qps"]), mb(ex["memory_estimate_bytes"])]]))
    out.append("\nFastest IVF operating point reaching each recall@10 target (nlist / nprobe):\n\n")
    out.append(table(["target", "nlist / nprobe", "recall@10", "p50", "p99", "vectors scored", "p50 speed-up vs exact"],
                     best_points(ds)))
    out.append("\nIVF builds:\n\n")
    out.append(table(["nlist", "build", "k-means iterations", "converged", "mean cos to centroid", "list size min / mean / max",
                      "imbalance", "memory (est.)"],
                     [[b["nlist"], f"{b['build_ms'] / 1000:.1f} s", b["iterations"], "yes" if b["converged"] else "no",
                       f"{b['objective_mean_cosine']:.3f}",
                       f"{b['balance']['min']} / {b['balance']['mean']:.0f} / {b['balance']['max']}",
                       f"{b['balance']['imbalance_factor']:.2f}", mb(b["memory_estimate_bytes"])] for b in ds["ivf_builds"]]))
    if full:
        deep = any(r.get("recall_at_100") for r in ds["ivf"])
        headers = ["nlist", "nprobe", "recall@1", "recall@10"] + (["recall@100"] if deep else []) + \
                  ["p50", "p95", "p99", "QPS", "vectors scored"]
        rows = []
        for r in ds["ivf"]:
            row = [r["nlist"], r["nprobe"], f"{r['recall_at_1']:.3f}", f"{r['recall_at_10']:.3f}"]
            if deep:
                row.append(f"{r.get('recall_at_100', 0):.3f}")
            row += [us(r["latency"]["p50_us"]), us(r["latency"]["p95_us"]), us(r["latency"]["p99_us"]), num(r["qps"]),
                    f"{100 * r['fraction_of_vectors_scored']:.1f} %"]
            rows.append(row)
        out.append("\n<details><summary>All operating points</summary>\n\n" + table(headers, rows) + "\n</details>\n")
    series = []
    for nl in sorted({r["nlist"] for r in ds["ivf"]}):
        series.append((f"nlist={nl}", [(r["latency"]["p50_us"], r["recall_at_10"]) for r in ds["ivf"] if r["nlist"] == nl]))
    series.append(("exact", [(ex["latency"]["p50_us"], 1.0)]))
    chart = f"{chart_prefix}-{name}.svg"
    line_chart(IMG / chart, f"{name}: recall@10 vs p50 latency", series, "p50 latency per query (µs, log scale)",
               "recall@10 vs exact", log_x=True)
    out.append(f"\n![{name} recall vs latency](img/{chart})\n")
    if ds.get("topk"):
        out.append("\nEffect of k:\n\n")
        out.append(table(["k", "strategy", "nprobe", "recall@k", "p50", "p99", "QPS"],
                         [[r["k"], r["strategy"], r.get("nprobe") or "", f"{r['recall_at_k']:.3f}", us(r["latency"]["p50_us"]),
                           us(r["latency"]["p99_us"]), num(r["qps"])] for r in ds["topk"]]))
    if ds.get("concurrency"):
        out.append("\nConcurrent searches against one shared read-only index:\n\n")
        out.append(table(["strategy", "nprobe", "goroutines", "QPS", "speed-up vs 1", "p50", "p99"],
                         [[r["strategy"], r.get("nprobe") or "", r["workers"], num(r["qps"]), f"{r['speedup_vs_1_worker']:.2f}×",
                           us(r["latency"]["p50_us"]), us(r["latency"]["p99_us"])] for r in ds["concurrency"]]))
        series = []
        for strat in sorted({r["strategy"] for r in ds["concurrency"]}):
            series.append((strat, [(r["workers"], r["qps"]) for r in ds["concurrency"] if r["strategy"] == strat]))
        chart = f"concurrency-{name}.svg"
        line_chart(IMG / chart, f"{name}: throughput vs goroutines", series, "concurrent goroutines", "queries per second",
                   y_range=(0, max(r["qps"] for r in ds["concurrency"]) * 1.08))
        out.append(f"\n![{name} concurrency](img/{chart})\n")
    out.append("\n")


def storage_section(s: dict, out: list[str]) -> None:
    d = s["dataset"]
    out.append(f"{num(d['n'])} vectors × {d['dim']} dims in a fresh byodb file "
               f"({s['vectors_per_transaction']} vectors per transaction).\n\n")
    c = s["single_chunk_upsert_latency"]
    rows = [
        ["bulk insert", f"{s['bulk_insert_ms'] / 1000:.1f} s ({num(s['bulk_insert_vectors_per_sec'])} vectors/s)"],
        ["single-chunk upsert (one commit each)", f"p50 {us(c['p50_us'])}, p99 {us(c['p99_us'])} (n={c['n']})"],
        ["file size", f"{mb(s['file_bytes'])} ({num(s['file_bytes_per_vector'])} bytes/vector; raw float32 = {mb(s['raw_vector_bytes'])})"],
        ["pages", f"{num(s['page_count'])} total, {num(s['free_pages'])} free"],
        [f"IVF train (nlist={s['ivf_nlist']})", f"{s['ivf_train_ms'] / 1000:.1f} s"],
        ["IVF persist (centroids + postings, one transaction)", f"{s['ivf_persist_ms'] / 1000:.2f} s"],
        ["cold open (read-only)", f"{s['cold_open_ms']:.1f} ms"],
        ["cold load: stream all vectors into the flat index", f"{s['cold_load_vectors_ms'] / 1000:.2f} s"],
        ["cold load: rebuild IVF lists from persisted postings", f"{s['cold_load_ivf_ms'] / 1000:.2f} s"],
        ["reloaded IVF returns identical results", f"{'yes' if s['reloaded_ivf_results_identical'] else 'NO'} "
                                                   f"({s['reload_check_queries']} queries)"],
    ]
    out.append(table(["measurement", "value"], rows))
    out.append("\n" + table(["table", "rows"], [[k, num(v)] for k, v in sorted(s["table_rows"].items())]) + "\n")


def ann_relevance_section(rep: dict, out: list[str]) -> None:
    for g in rep["groups"]:
        out.append(f"### {g['corpus']} / {g['model']}\n\n{g['queries']} {g['split']} queries, {num(g['corpus_size'])} documents.\n\n")
        rows = []
        for r in g["rows"]:
            m = r["metrics"]
            cmp = r.get("vs_exact")
            rows.append([r["strategy"], r["nlist"] or "", r["nprobe"] or "", f"{r['ann_recall_at_10']:.3f}",
                         f"{m['nDCG@10']:.4f}", f"{m['MRR@10']:.4f}", f"{m['Recall@10']:.4f}", f"{m['Recall@100']:.4f}",
                         f"{cmp['mean_diff']:+.4f} [{cmp['ci95'][0]:+.4f}, {cmp['ci95'][1]:+.4f}]" if cmp else ""])
        if g.get("bm25"):
            m = g["bm25"]["metrics"]
            rows.append(["BM25", "", "", "", f"{m['nDCG@10']:.4f}", f"{m['MRR@10']:.4f}", f"{m['Recall@10']:.4f}",
                         f"{m['Recall@100']:.4f}", ""])
        out.append(table(["strategy", "nlist", "nprobe", "ANN recall@10", "nDCG@10", "MRR@10", "Recall@10", "Recall@100",
                          "Δ nDCG@10 vs exact [95 % CI]"], rows))
        if g.get("bm25"):
            c = g["bm25"]["exact_vs_bm25"]
            out.append(f"\nExact dense − BM25: Δ nDCG@10 {c['mean_diff']:+.4f}, 95 % CI [{c['ci95'][0]:+.4f}, {c['ci95'][1]:+.4f}], "
                       f"{c['improved']} improved / {c['worsened']} worsened / {c['unchanged']} unchanged.\n")
        ivf = [r for r in g["rows"] if r["strategy"] == "ivf"]
        chart = f"ann-relevance-{g['corpus']}-{g['model']}.svg"
        line_chart(IMG / chart, f"{g['corpus']} / {g['model']}: ANN recall vs relevance",
                   [("ANN recall@10", [(r["nprobe"], r["ann_recall_at_10"]) for r in ivf]),
                    ("nDCG@10", [(r["nprobe"], r["metrics"]["nDCG@10"]) for r in ivf]),
                    ("exact nDCG@10", [(ivf[0]["nprobe"], g["rows"][0]["metrics"]["nDCG@10"]),
                                       (ivf[-1]["nprobe"], g["rows"][0]["metrics"]["nDCG@10"])])],
                   "nprobe (log scale)", "score", log_x=True, y_range=(0, 1))
        out.append(f"\n![{g['corpus']} ANN recall vs relevance](img/{chart})\n\n")


def api_section(rep: dict, out: list[str]) -> None:
    srv = rep["server"]
    out.append(f"{rep['queries']} queries × {rep['repeats']} repeats against a server holding {num(srv['vectors'])} vectors "
               f"({num(srv.get('documents') or 0)} documents); model `{srv['health']['model_id']}`.\n\n")
    rows = []
    for r in rep["serial"]:
        s = r["server_ms"]
        rows.append([r["strategy"], r["nprobe"] or "", f"{r['client_wall_ms']['p50']:.2f}", f"{r['client_wall_ms']['p99']:.2f}",
                     f"{s['embed_ms']['p50']:.2f}", f"{s['search_ms']['p50']:.3f}", f"{s['fetch_ms']['p50']:.3f}",
                     f"{s['total_ms']['p50']:.2f}"])
    out.append(table(["strategy", "nprobe", "client p50 ms", "client p99 ms", "embed p50 ms", "index search p50 ms",
                      "text fetch p50 ms", "server total p50 ms"], rows))
    out.append("\n" + table(["strategy", "clients", "QPS", "p50 ms", "p99 ms", "errors"],
                            [[r["strategy"], r["workers"], f"{r['qps']:.1f}", f"{r['client_wall_ms']['p50']:.1f}",
                              f"{r['client_wall_ms']['p99']:.1f}", r["errors"]] for r in rep["concurrent"]]) + "\n")


def main() -> None:
    out = ["# VectorForge benchmark tables\n",
           "Generated by `vectorforge/scripts/benchreport.py` from the JSON in `vectorforge/results/`. "
           "Do not edit by hand; rerun the script. Narrative and interpretation: [benchmarks.md](benchmarks.md).\n"]
    for suite in ("synthetic", "dims", "real"):
        rep = load(BENCH / f"{suite}.json")
        if not rep:
            continue
        out.append(f"\n## {suite}\n\n" + env_line(rep) + "\n")
        for ds in rep["datasets"]:
            dataset_section(ds, "recall-latency", out, full=True)
    rep = load(BENCH / "dims.json")
    if rep:
        series = [(ds["dataset"]["name"], [(r["nprobe"], r["latency"]["p50_us"]) for r in ds["ivf"]]) for ds in rep["datasets"]]
        line_chart(IMG / "dims-latency.svg", "IVF p50 latency vs nprobe by dimension (100k vectors, nlist=316)", series,
                   "nprobe (log scale)", "p50 latency (µs)", log_x=True,
                   y_range=(0, max(p[1] for _, s in series for p in s) * 1.08))
        out.append("![dimension sweep](img/dims-latency.svg)\n")
    rep = load(BENCH / "storage.json")
    if rep:
        out.append("\n## storage\n\n" + env_line(rep) + "\n")
        storage_section(rep["storage"], out)
    rep = load(ML / "ann-relevance.json")
    if rep:
        out.append("\n## ANN recall vs relevance (real embeddings, BEIR labels)\n\n")
        ann_relevance_section(rep, out)
    apis = sorted((load(p) for p in BENCH.glob("api*.json")), key=lambda r: r.get("measured_at", ""))
    if apis:
        out.append("\n## HTTP API end to end\n\n")
        for rep in apis:
            out.append(f"### {rep.get('label') or 'run'} ({rep.get('measured_at', '')})\n\n")
            api_section(rep, out)
    (DOCS / "benchmark-tables.md").write_text("\n".join(out), encoding="utf-8")
    print(f"wrote {DOCS / 'benchmark-tables.md'} and {len(list(IMG.glob('*.svg')))} charts in {IMG}")


if __name__ == "__main__":
    main()
