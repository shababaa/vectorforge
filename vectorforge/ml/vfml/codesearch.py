"""Code-search relevance on the labelled query set, against a running server.

  python -m vfml.codesearch --url http://127.0.0.1:8080

Compares, at file level, on exactly the chunks stored in VectorForge:
  * BM25 over the same text the encoder sees (path, section header, chunk),
    with identifier splitting (camelCase / snake_case) for code;
  * dense exact search through the API;
  * dense IVF search through the API at several nprobe values, with ANN
    recall@10 against exact search reported next to relevance, so the two
    can be told apart.

Chunk rankings are collapsed to files by first occurrence. The labels are a
small assistant-authored set (see vectorforge/eval/codesearch.json); results
are indicative only.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import time
import urllib.request
from pathlib import Path

import numpy as np

from .bm25 import BM25
from .metrics import dedupe, evaluate, paired_bootstrap, per_query

ROOT = Path(__file__).resolve().parents[3]
QUERIES = ROOT / "vectorforge" / "eval" / "codesearch.json"
OUT = ROOT / "vectorforge" / "results" / "ml" / "codesearch.json"
KS = (1, 5, 10)
DEPTH = 100  # chunks retrieved per query before collapsing to files


def call(url: str, method: str, path: str, body: dict | None = None) -> dict:
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=120) as resp:
        return json.load(resp)["data"]


def embedding_text(path: str, section: str, text: str) -> str:
    # Mirrors ingest.EmbeddingText in Go.
    return f"{path}\n{text}" if not section else f"{path} · {section}\n{text}"


def fetch_corpus(url: str) -> tuple[list[str], list[str], list[int], dict]:
    """Returns per-chunk (file key, text, vector id) and corpus metadata."""
    docs, offset = [], 0
    while True:
        page = call(url, "GET", f"/api/v1/documents?limit=500&offset={offset}")
        docs += page["documents"]
        offset += len(page["documents"])
        if offset >= page["total"] or not page["documents"]:
            break
    keys, texts, vids = [], [], []
    for d in sorted(docs, key=lambda d: d["doc_id"]):
        for ch in call(url, "GET", f"/api/v1/documents/{d['doc_id']}")["chunks"]:
            keys.append(f"{d['source']}:{d['path']}")
            texts.append(embedding_text(d["path"], ch["section"], ch["text"]))
            vids.append(ch["vector_id"])
    digest = hashlib.sha256("\n".join(f"{d['source']}:{d['path']}:{d['content_hash']}" for d in
                                      sorted(docs, key=lambda d: (d["source"], d["path"]))).encode()).hexdigest()
    meta = {"documents": len(docs), "chunks": len(keys), "content_digest": digest,
            "sources": sorted({d["source"] for d in docs}), "chunker": sorted({d["chunker_version"] for d in docs}),
            "model": sorted({d["model_id"] for d in docs})}
    return keys, texts, vids, meta


def dense(url: str, queries: list[dict], strategy: str, nprobe: int | None = None) -> tuple[dict, dict, dict]:
    run, chunk_ids, timing = {}, {}, []
    for q in queries:
        body = {"query": q["query"], "top_k": DEPTH, "strategy": strategy}
        if nprobe:
            body["nprobe"] = nprobe
        r = call(url, "POST", "/api/v1/search", body)
        run[q["id"]] = dedupe(f"{h['source']}:{h['path']}" for h in r["results"])
        chunk_ids[q["id"]] = [h["vector_id"] for h in r["results"]]
        timing.append(r["timing"]["search_ms"])
    t = np.array(timing)
    return run, chunk_ids, {"search_ms_p50": float(np.percentile(t, 50)), "search_ms_mean": float(t.mean())}


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--url", default="http://127.0.0.1:8080")
    p.add_argument("--queries", default=str(QUERIES))
    p.add_argument("--nprobe", default="1,2,4,8,16,32")
    p.add_argument("--out", default=str(OUT))
    args = p.parse_args()

    spec = json.loads(Path(args.queries).read_text())
    queries = spec["queries"]
    qrels = {q["id"]: q["relevant"] for q in queries}
    keys, texts, vids, corpus = fetch_corpus(args.url)
    missing = sorted({d for q in queries for d in q["relevant"]} - set(keys))
    if missing:
        raise SystemExit(f"labelled files not in the index (re-ingest the demo sources): {missing}")
    stats = call(args.url, "GET", "/api/v1/index/stats")

    t0 = time.perf_counter()
    bm25 = BM25(texts, split_identifiers=True)
    bm25_index_s = time.perf_counter() - t0
    runs: dict[str, dict] = {}
    runs["bm25"] = {q["id"]: dedupe(keys[i] for i, _ in bm25.search(q["query"], DEPTH)) for q in queries}

    exact_run, exact_chunks, exact_timing = dense(args.url, queries, "exact")
    runs["dense-exact"] = exact_run
    timings = {"dense-exact": exact_timing}
    ann_recall = {}
    for np_ in [int(x) for x in args.nprobe.split(",") if x]:
        if not stats["ivf"]["trained"] or np_ > stats["ivf"]["meta"]["nlist"]:
            continue
        name = f"dense-ivf-nprobe{np_}"
        runs[name], chunks, timings[name] = dense(args.url, queries, "ivf", np_)
        ann_recall[name] = float(np.mean([len(set(exact_chunks[q][:10]) & set(chunks[q][:10])) / max(1, min(10, len(exact_chunks[q])))
                                          for q in exact_chunks]))

    def scores(run: dict, subset: set[str] | None = None) -> dict:
        ids = [q["id"] for q in queries if subset is None or q["id"] in subset]
        return evaluate({i: run[i] for i in ids}, {i: qrels[i] for i in ids}, KS)

    styles = {s: {q["id"] for q in queries if q["style"] == s} for s in {q["style"] for q in queries}}
    sources = {"go-stdlib": {q["id"] for q in queries if q["id"].startswith("std-")},
               "byodb": {q["id"] for q in queries if q["id"].startswith("repo-")}}
    report = {
        "query_set": {"name": spec["name"], "queries": len(queries), "provenance": spec["provenance"],
                      "styles": {k: len(v) for k, v in styles.items()}},
        "corpus": corpus,
        "index": {"vectors": stats["vectors"], "nlist": stats["ivf"]["meta"]["nlist"] if stats["ivf"]["trained"] else None},
        "git_commit": subprocess.run(["git", "rev-parse", "--short", "HEAD"], capture_output=True, text=True, cwd=ROOT).stdout.strip(),
        "protocol": {"unit": "file (chunk rankings collapsed by first occurrence)", "chunks_retrieved": DEPTH,
                     "bm25": {"k1": bm25.k1, "b": bm25.b, "split_identifiers": True, "index_seconds": bm25_index_s},
                     "dense": "query embedded by the running service; exact = brute-force cosine"},
        "metrics": {name: scores(run) for name, run in runs.items()},
        "by_style": {s: {name: scores(run, ids) for name, run in runs.items() if name in ("bm25", "dense-exact")} for s, ids in styles.items()},
        "by_source": {s: {name: scores(run, ids) for name, run in runs.items() if name in ("bm25", "dense-exact")} for s, ids in sources.items()},
        "ann_recall_at_10_chunks": ann_recall,
        "search_timing": timings,
        "comparisons": {},
        "per_query": {q["id"]: {name: {"rank_of_first_relevant": next((i + 1 for i, d in enumerate(run[q["id"]]) if d in qrels[q["id"]]), None)}
                                for name, run in runs.items() if name in ("bm25", "dense-exact")} for q in queries},
    }
    per = {name: per_query(run, qrels, "ndcg", 10) for name, run in runs.items()}
    report["comparisons"]["dense-exact_vs_bm25"] = {"metric": "nDCG@10", **paired_bootstrap(per["bm25"], per["dense-exact"], seed=1)}
    for name in runs:
        if name.startswith("dense-ivf"):
            report["comparisons"][f"{name}_vs_dense-exact"] = {"metric": "nDCG@10", **paired_bootstrap(per["dense-exact"], per[name], seed=1)}

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2) + "\n")
    for name, m in report["metrics"].items():
        extra = f"  ANN recall@10 {ann_recall[name]:.3f}" if name in ann_recall else ""
        print(f"{name:22s} nDCG@10 {m['nDCG@10']:.3f}  MRR@10 {m['MRR@10']:.3f}  R@10 {m['Recall@10']:.3f}{extra}")
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
