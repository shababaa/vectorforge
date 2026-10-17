"""Single-query encode latency of several models, measured interleaved.

  python -m vfml.encode_latency

scifact.py measures each model's latency right after its own training, so
those numbers are taken minutes apart under whatever else the machine was
doing. This script loads every model first and alternates between them on the
same query texts, round by round, so drift affects all models equally. Only
query texts are read (no labels).
"""

from __future__ import annotations

import argparse
import json
import platform
import time
from pathlib import Path

import numpy as np
import torch
from sentence_transformers import SentenceTransformer

from . import beir
from .encoder import DEFAULT_MODEL, resolve_model

ML_ROOT = Path(__file__).resolve().parent.parent
RUNS = ML_ROOT / "artifacts" / "scifact" / "runs"


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--queries", type=int, default=100)
    p.add_argument("--rounds", type=int, default=5)
    p.add_argument("--out", type=Path, default=ML_ROOT.parent / "results" / "ml" / "encode-latency.json")
    args = p.parse_args()

    ds = beir.load("scifact")
    texts = [ds.queries[q] for q in sorted(ds.qrels["test"])[: args.queries]]
    models = {"pretrained": SentenceTransformer(resolve_model(DEFAULT_MODEL)[0], device="cpu")}
    for name in ("hardneg", "inbatch"):
        if (RUNS / name / "best").is_dir():
            models[f"finetuned-{name}"] = SentenceTransformer(str(RUNS / name / "best"), device="cpu")
    for m in models.values():
        m.encode(texts[:10], show_progress_bar=False)
    samples: dict[str, list[float]] = {name: [] for name in models}
    for _ in range(args.rounds):
        for name, m in models.items():
            for t in texts:
                t0 = time.perf_counter()
                m.encode([t], show_progress_bar=False, normalize_embeddings=True)
                samples[name].append((time.perf_counter() - t0) * 1000)
    report = {
        "methodology": __doc__.strip(),
        "environment": {"python": platform.python_version(), "platform": platform.platform(),
                        "torch": torch.__version__, "torch_threads": torch.get_num_threads()},
        "queries": len(texts), "rounds": args.rounds, "batch_size": 1,
        "mean_query_chars": float(np.mean([len(t) for t in texts])),
        "latency_ms": {},
    }
    for name, s in samples.items():
        a = np.asarray(s)
        report["latency_ms"][name] = {"n": int(a.size), "mean": float(a.mean()), "p50": float(np.percentile(a, 50)),
                                      "p95": float(np.percentile(a, 95)), "p99": float(np.percentile(a, 99))}
        print(name, {k: round(v, 2) for k, v in report["latency_ms"][name].items()})
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    print(f"wrote {args.out}")


if __name__ == "__main__":
    main()
