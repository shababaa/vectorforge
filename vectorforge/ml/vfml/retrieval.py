"""Dense and lexical retrieval runs for offline evaluation."""

from __future__ import annotations

import time

import numpy as np

from .bm25 import BM25
from .preprocess import normalize_text


def encode(model, texts: list[str], batch_size: int = 64, prompt_name: str | None = None) -> np.ndarray:
    """Unit-normalized float32 embeddings, with the service's preprocessing."""
    cleaned = [normalize_text(t) or " " for t in texts]
    vectors = model.encode(
        cleaned,
        batch_size=batch_size,
        normalize_embeddings=True,
        convert_to_numpy=True,
        show_progress_bar=False,
        prompt_name=prompt_name,
    ).astype(np.float32)
    norms = np.linalg.norm(vectors.astype(np.float64), axis=1, keepdims=True)
    return (vectors / norms).astype(np.float32)


def dense_run(query_vecs: np.ndarray, query_ids: list[str], doc_vecs: np.ndarray, doc_ids: list[str], k: int = 100):
    """Exact top-k by dot product (cosine for unit vectors), ties by doc index."""
    scores = query_vecs @ doc_vecs.T
    k = min(k, len(doc_ids))
    run = {}
    for i, qid in enumerate(query_ids):
        row = scores[i]
        top = np.argpartition(-row, k - 1)[:k]
        order = top[np.lexsort((top, -row[top]))]
        run[qid] = [doc_ids[j] for j in order]
    return run


def bm25_run(bm25: BM25, queries: dict[str, str], query_ids: list[str], doc_ids: list[str], k: int = 100):
    return {qid: [doc_ids[i] for i, _ in bm25.search(queries[qid], k)] for qid in query_ids}


def query_latency_ms(model, texts: list[str], repeats: int = 3) -> dict[str, float]:
    """Single-query CPU encode latency percentiles (batch size 1)."""
    model.encode(texts[:5], show_progress_bar=False)  # warm-up
    samples = []
    for _ in range(repeats):
        for t in texts:
            started = time.perf_counter()
            model.encode([t], show_progress_bar=False, normalize_embeddings=True)
            samples.append((time.perf_counter() - started) * 1000)
    a = np.asarray(samples)
    return {"n": int(a.size), "p50": float(np.percentile(a, 50)), "p95": float(np.percentile(a, 95)),
            "p99": float(np.percentile(a, 99)), "mean": float(a.mean())}
