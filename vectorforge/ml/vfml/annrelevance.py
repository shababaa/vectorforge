"""Score vfbench run files against relevance labels.

  python -m vfml.annrelevance vectorforge/results/bench/runs --out vectorforge/results/ml

vfbench -suite real writes, per exported model, one top-100 run for exact
search and one per IVF nprobe. This joins them with the BEIR qrels so the
cost of approximate search is reported in relevance terms (nDCG@10, MRR@10,
Recall@10/100) next to ANN recall against the exact run.

Following BEIR, a document whose ID equals the query ID is dropped from the
ranking before scoring (Quora's queries are also corpus documents).
"""

from __future__ import annotations

import argparse
import json
from collections import defaultdict
from pathlib import Path

from . import beir
from .bm25 import BM25
from .metrics import evaluate, paired_bootstrap, per_query
from .retrieval import bm25_run


def load_runs(run_dir: Path) -> dict[tuple[str, str], list[dict]]:
    groups: dict[tuple[str, str], list[dict]] = defaultdict(list)
    for path in sorted(run_dir.glob("*.json")):
        rf = json.loads(path.read_text())
        if rf.get("corpus"):
            rf["file"] = path.name
            groups[(rf["corpus"], rf["model"])].append(rf)
    return groups


def ann_recall(run: dict[str, list[str]], exact: dict[str, list[str]], k: int) -> float:
    total = 0.0
    for qid, ranked in exact.items():
        truth = set(ranked[:k])
        total += len(truth & set(run.get(qid, [])[:k])) / max(1, len(truth))
    return total / max(1, len(exact))


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("runs", type=Path)
    p.add_argument("--out", type=Path, default=Path("vectorforge/results/ml"))
    p.add_argument("--bm25", action="store_true", help="also score a BM25 baseline on the same queries")
    args = p.parse_args()

    groups = load_runs(args.runs)
    if not groups:
        raise SystemExit(f"no vfbench run files with a corpus field in {args.runs}")
    datasets: dict[str, beir.Dataset] = {}
    bm25_runs: dict[tuple[str, tuple[str, ...]], dict[str, list[str]]] = {}
    report = {"methodology": __doc__.strip(), "groups": []}
    for (corpus, model), files in sorted(groups.items()):
        if corpus not in datasets:
            datasets[corpus] = beir.load(corpus)
        ds = datasets[corpus]
        split = files[0].get("split") or "test"
        exact_file = next((f for f in files if f["strategy"] == "exact"), None)
        if exact_file is None:
            raise SystemExit(f"{corpus}/{model}: no exact run")
        qids = sorted(exact_file["runs"])
        qrels = {q: ds.qrels[split][q] for q in qids}

        def clean(run: dict[str, list[str]]) -> dict[str, list[str]]:
            return {q: [d for d in ranked if d != q] for q, ranked in run.items()}

        exact = clean(exact_file["runs"])
        exact_pq = per_query(exact, qrels, "ndcg", 10)
        rows = []
        for f in sorted(files, key=lambda f: (f["strategy"] != "exact", f.get("nprobe", 0))):
            run = clean(f["runs"])
            row = {
                "strategy": f["strategy"], "nlist": f.get("nlist", 0), "nprobe": f.get("nprobe", 0),
                "metrics": evaluate(run, qrels),
                "ann_recall_at_10": ann_recall(run, exact, 10),
                "ann_recall_at_100": ann_recall(run, exact, 100),
            }
            if f["strategy"] != "exact":
                row["vs_exact"] = {"metric": "nDCG@10",
                                   **paired_bootstrap(exact_pq, per_query(run, qrels, "ndcg", 10), seed=1)}
            rows.append(row)
        group = {"corpus": corpus, "model": model, "split": split, "queries": len(qids),
                 "corpus_size": len(ds.corpus), "rows": rows}
        if args.bm25:
            key = (corpus, tuple(qids))
            if key not in bm25_runs:
                doc_ids = sorted(ds.corpus)
                index = BM25([ds.doc_text(d) for d in doc_ids])
                bm25_runs[key] = clean(bm25_run(index, ds.queries, qids, doc_ids))
            bm = bm25_runs[key]
            group["bm25"] = {"metrics": evaluate(bm, qrels),
                             "exact_vs_bm25": {"metric": "nDCG@10",
                                               **paired_bootstrap(per_query(bm, qrels, "ndcg", 10), exact_pq, seed=1)}}
            print(f"{corpus}: BM25 nDCG@10={group['bm25']['metrics']['nDCG@10']:.4f}")
        report["groups"].append(group)
        best = rows[0]["metrics"]
        print(f"{corpus}/{model}: exact nDCG@10={best['nDCG@10']:.4f} over {len(qids)} queries")
        for r in rows[1:]:
            print(f"  nlist={r['nlist']} nprobe={r['nprobe']:>3}  ANN R@10={r['ann_recall_at_10']:.3f}  "
                  f"nDCG@10={r['metrics']['nDCG@10']:.4f}")
    args.out.mkdir(parents=True, exist_ok=True)
    path = args.out / "ann-relevance.json"
    path.write_text(json.dumps(report, indent=2) + "\n")
    print(f"wrote {path}")


if __name__ == "__main__":
    main()
