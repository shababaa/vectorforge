"""Okapi BM25 lexical retrieval, implemented with an in-memory inverted index.

score(q, d) = sum_{t in q} idf(t) * tf(t,d) * (k1 + 1) / (tf(t,d) + k1 * (1 - b + b * |d| / avgdl))
idf(t)      = ln(1 + (N - df(t) + 0.5) / (df(t) + 0.5))

Defaults k1=0.9, b=0.4 follow the Anserini settings commonly used for BEIR.
There is no stemming, so scores differ from Elasticsearch/Lucene baselines.
"""

from __future__ import annotations

import math
import re
from collections import Counter, defaultdict

import numpy as np

STOPWORDS = frozenset(
    "a an and are as at be but by for from has have if in into is it its of on or such that the their then "
    "there these they this to was were will with what which who how why when where does do can".split()
)

_word = re.compile(r"[A-Za-z0-9]+")
_camel = re.compile(r"[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z]+|[A-Z]+|[0-9]+")


def tokenize(text: str, split_identifiers: bool = False) -> list[str]:
    """Lowercased alphanumeric tokens without stopwords.

    With split_identifiers, `seekLE` and `delete_range` also contribute their
    parts (`seek`, `le`, `delete`, `range`), which matters for code search.
    """
    tokens: list[str] = []
    for raw in _word.findall(text):
        lower = raw.lower()
        if lower not in STOPWORDS:
            tokens.append(lower)
        if split_identifiers:
            parts = [p.lower() for p in _camel.findall(raw)]
            if len(parts) > 1:
                tokens.extend(p for p in parts if p not in STOPWORDS)
    if split_identifiers:
        for ident in re.findall(r"[A-Za-z0-9]+(?:_[A-Za-z0-9]+)+", text):
            tokens.extend(p.lower() for p in ident.split("_") if p and p.lower() not in STOPWORDS)
    return tokens


class BM25:
    def __init__(self, documents: list[str], k1: float = 0.9, b: float = 0.4, split_identifiers: bool = False):
        self.k1, self.b, self.split = k1, b, split_identifiers
        self.n = len(documents)
        lengths = np.zeros(self.n, dtype=np.float64)
        postings: dict[str, list[tuple[int, int]]] = defaultdict(list)
        for i, doc in enumerate(documents):
            counts = Counter(tokenize(doc, split_identifiers))
            lengths[i] = sum(counts.values())
            for term, tf in counts.items():
                postings[term].append((i, tf))
        self.avgdl = float(lengths.mean()) if self.n else 0.0
        self._norm = k1 * (1 - b + b * lengths / max(self.avgdl, 1e-9))
        self._index = {
            t: (np.fromiter((d for d, _ in p), np.int64, len(p)), np.fromiter((f for _, f in p), np.float64, len(p)))
            for t, p in postings.items()
        }

    def idf(self, term: str) -> float:
        df = len(self._index[term][0]) if term in self._index else 0
        return math.log(1 + (self.n - df + 0.5) / (df + 0.5))

    def scores(self, query: str) -> np.ndarray:
        scores = np.zeros(self.n, dtype=np.float64)
        for term in set(tokenize(query, self.split)):
            if term not in self._index:
                continue
            docs, tf = self._index[term]
            scores[docs] += self.idf(term) * tf * (self.k1 + 1) / (tf + self._norm[docs])
        return scores

    def search(self, query: str, k: int) -> list[tuple[int, float]]:
        """Top-k (document index, score) with score > 0, ties broken by index."""
        scores = self.scores(query)
        order = np.lexsort((np.arange(self.n), -scores))[: max(k, 0)]
        return [(int(i), float(scores[i])) for i in order if scores[i] > 0]
