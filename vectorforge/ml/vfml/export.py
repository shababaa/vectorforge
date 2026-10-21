"""Embed a BEIR dataset and export raw matrices for cmd/vfbench.

  python -m vfml.export quora --queries 1000

Writes artifacts/<dataset>/<model-name>/export/{corpus.f32,queries.f32,meta.json}
in the format that `vfbench -suite real` reads. Queries are a seeded sample
of the test split; only the queries and corpus are exported, never labels.
Labels are used later, by vfml.annrelevance, to score the run files.
"""

from __future__ import annotations

import argparse
import json
import random
import time
from pathlib import Path

from sentence_transformers import SentenceTransformer

from . import beir
from .encoder import DEFAULT_MODEL, resolve_model
from .retrieval import encode

ARTIFACTS = Path(__file__).resolve().parent.parent / "artifacts"


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("dataset")
    p.add_argument("--model", default=DEFAULT_MODEL)
    p.add_argument("--name", default="pretrained", help="label for the model in file names")
    p.add_argument("--split", default="test")
    p.add_argument("--queries", type=int, default=1000)
    p.add_argument("--seed", type=int, default=2027)
    p.add_argument("--batch-size", type=int, default=256)
    args = p.parse_args()

    ds = beir.load(args.dataset)
    doc_ids = sorted(ds.corpus)
    judged = sorted(ds.qrels[args.split])
    query_ids = sorted(random.Random(args.seed).sample(judged, min(args.queries, len(judged))))
    path, model_id = resolve_model(args.model)
    model = SentenceTransformer(path, device="cpu")
    print(f"{args.dataset}: {len(doc_ids)} documents, {len(query_ids)} of {len(judged)} {args.split} queries; model {model_id}", flush=True)

    t0 = time.perf_counter()
    doc_vecs = encode(model, [ds.doc_text(d) for d in doc_ids], batch_size=args.batch_size)
    corpus_s = time.perf_counter() - t0
    query_vecs = encode(model, [ds.queries[q] for q in query_ids], batch_size=args.batch_size)
    print(f"encoded corpus in {corpus_s:.0f} s ({len(doc_ids) / corpus_s:.0f} docs/s)", flush=True)

    out = ARTIFACTS / args.dataset / args.name / "export"
    out.mkdir(parents=True, exist_ok=True)
    doc_vecs.astype("<f4").tofile(out / "corpus.f32")
    query_vecs.astype("<f4").tofile(out / "queries.f32")
    (out / "meta.json").write_text(json.dumps({
        "dataset": args.dataset, "model": args.name, "model_id": model_id, "dimension": int(doc_vecs.shape[1]),
        "doc_ids": doc_ids, "query_ids": query_ids, "split": args.split, "query_sample_seed": args.seed,
        "corpus_encode_seconds": corpus_s}))
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
