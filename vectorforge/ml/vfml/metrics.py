"""Ranking metrics for retrieval evaluation against labelled relevance.

Conventions (matching pytrec_eval / BEIR so numbers are comparable):
  * qrels map query -> {doc: grade}; grade > 0 means relevant.
  * Queries without any relevant document are excluded from averages.
  * nDCG uses linear gain: DCG@k = sum_{i=1..k} grade_i / log2(i + 1).
  * Metrics are macro-averaged: computed per query, then averaged.
"""

from __future__ import annotations

import math
from collections.abc import Iterable, Mapping, Sequence

Qrels = Mapping[str, Mapping[str, int]]
Run = Mapping[str, Sequence[str]]


def reciprocal_rank(ranked: Sequence[str], relevant: Mapping[str, int], k: int) -> float:
    """1/rank of the first relevant document within the top k, else 0."""
    for i, doc in enumerate(ranked[:k], start=1):
        if relevant.get(doc, 0) > 0:
            return 1.0 / i
    return 0.0


def recall(ranked: Sequence[str], relevant: Mapping[str, int], k: int) -> float:
    """Fraction of all relevant documents that appear in the top k."""
    positives = {d for d, g in relevant.items() if g > 0}
    if not positives:
        raise ValueError("recall is undefined without relevant documents")
    return len(positives.intersection(ranked[:k])) / len(positives)


def precision(ranked: Sequence[str], relevant: Mapping[str, int], k: int) -> float:
    return sum(1 for d in ranked[:k] if relevant.get(d, 0) > 0) / k


def ndcg(ranked: Sequence[str], relevant: Mapping[str, int], k: int) -> float:
    """Normalized discounted cumulative gain at k with graded relevance."""
    dcg = sum(relevant.get(d, 0) / math.log2(i + 1) for i, d in enumerate(ranked[:k], start=1))
    ideal = sorted((g for g in relevant.values() if g > 0), reverse=True)[:k]
    idcg = sum(g / math.log2(i + 1) for i, g in enumerate(ideal, start=1))
    if idcg == 0:
        raise ValueError("nDCG is undefined without relevant documents")
    return dcg / idcg


def dedupe(ranked: Iterable[str]) -> list[str]:
    """Keep the first occurrence of each id (e.g. collapse chunks to files)."""
    seen: set[str] = set()
    out = []
    for item in ranked:
        if item not in seen:
            seen.add(item)
            out.append(item)
    return out


def per_query(run: Run, qrels: Qrels, metric: str, k: int) -> dict[str, float]:
    """One metric value per judged query, for paired significance tests."""
    fn = {"ndcg": ndcg, "recall": recall, "mrr": reciprocal_rank}[metric]
    return {q: fn(list(run.get(q, ())), rel, k) for q, rel in qrels.items() if any(g > 0 for g in rel.values())}


def paired_bootstrap(a: Mapping[str, float], b: Mapping[str, float], samples: int = 10000, seed: int = 0) -> dict:
    """Bootstrap the mean per-query difference b - a over the same queries.

    Resampling queries with replacement estimates how much the difference
    would vary with a different sample of queries from the same distribution.
    """
    import numpy as np

    keys = sorted(a)
    if set(keys) != set(b):
        raise ValueError("paired bootstrap needs the same queries on both sides")
    diff = np.asarray([b[k] - a[k] for k in keys])
    rng = np.random.default_rng(seed)
    idx = rng.integers(0, len(diff), size=(samples, len(diff)))
    means = diff[idx].mean(axis=1)
    return {
        "mean_diff": float(diff.mean()),
        "ci95": [float(np.percentile(means, 2.5)), float(np.percentile(means, 97.5))],
        "p_diff_le_0": float((means <= 0).mean()),
        "queries": len(diff),
        "improved": int((diff > 0).sum()),
        "worsened": int((diff < 0).sum()),
        "unchanged": int((diff == 0).sum()),
    }


def evaluate(run: Run, qrels: Qrels, ks: Sequence[int] = (1, 5, 10, 100)) -> dict[str, float]:
    """Macro-averaged nDCG@k, Recall@k, MRR@10 and P@1 over judged queries.

    A judged query missing from the run counts as a complete miss.
    """
    judged = [q for q, rel in qrels.items() if any(g > 0 for g in rel.values())]
    if not judged:
        raise ValueError("no judged queries")
    totals: dict[str, float] = {}

    def add(name: str, value: float) -> None:
        totals[name] = totals.get(name, 0.0) + value

    for q in judged:
        ranked = list(run.get(q, ()))
        rel = qrels[q]
        add("MRR@10", reciprocal_rank(ranked, rel, 10))
        add("P@1", precision(ranked, rel, 1))
        for k in ks:
            add(f"nDCG@{k}", ndcg(ranked, rel, k))
            add(f"Recall@{k}", recall(ranked, rel, k))
    result = {name: value / len(judged) for name, value in sorted(totals.items())}
    result["queries"] = float(len(judged))
    return result
