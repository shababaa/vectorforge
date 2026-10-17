"""SciFact retrieval experiment: BM25 vs pretrained vs fine-tuned MiniLM.

Protocol
  1. Split the BEIR SciFact *train* queries 85/15 into train and validation
     with a fixed seed. The 300 BEIR *test* queries are held out.
  2. Mine hard negatives with BM25 for training queries only.
  3. Fine-tune on the training split; after each epoch evaluate on the
     validation split and keep the epoch with the best nDCG@10.
  4. Evaluate BM25, the pretrained model and each selected checkpoint once on
     the test split, with paired bootstrap confidence intervals.

Test labels are never used for training, negative mining or model selection.

  python -m vfml.scifact                 # full experiment (~35 min on 6 CPU threads)
  python -m vfml.scifact --smoke         # tiny end-to-end check (~1 min)
"""

from __future__ import annotations

import argparse
import hashlib
import json
import platform
import random
import time
from dataclasses import asdict
from pathlib import Path

import numpy as np

from . import beir
from .bm25 import BM25
from .encoder import DEFAULT_MODEL, resolve_model
from .metrics import evaluate, paired_bootstrap, per_query
from .retrieval import bm25_run, dense_run, encode, query_latency_ms
from .train import TrainConfig, mine_hard_negatives, train

ML_ROOT = Path(__file__).resolve().parent.parent
RESULTS = ML_ROOT.parent / "results" / "ml"
ARTIFACTS = ML_ROOT / "artifacts" / "scifact"
SPLIT_SEED = 2027
VAL_FRACTION = 0.15
KS = (1, 5, 10, 100)


def split_queries(qids: list[str], fraction: float, seed: int) -> tuple[list[str], list[str]]:
    ordered = sorted(qids)
    random.Random(seed).shuffle(ordered)
    n_val = round(len(ordered) * fraction)
    return sorted(ordered[n_val:]), sorted(ordered[:n_val])


def ids_digest(ids: list[str]) -> str:
    return hashlib.sha256("\n".join(sorted(ids)).encode()).hexdigest()[:16]


def dir_bytes(path: Path) -> int:
    return sum(p.stat().st_size for p in Path(path).rglob("*") if p.is_file())


def export(name: str, doc_ids, doc_vecs, query_ids, query_vecs) -> str:
    """Raw little-endian float32 matrices for the Go benchmark harness."""
    out = ARTIFACTS / name / "export"
    out.mkdir(parents=True, exist_ok=True)
    doc_vecs.astype("<f4").tofile(out / "corpus.f32")
    query_vecs.astype("<f4").tofile(out / "queries.f32")
    (out / "meta.json").write_text(json.dumps({
        "dataset": "scifact", "model": name, "dimension": int(doc_vecs.shape[1]),
        "doc_ids": doc_ids, "query_ids": query_ids, "split": "test"}))
    return str(out)


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--epochs", type=int, default=3)
    p.add_argument("--configs", default="hardneg,inbatch")
    p.add_argument("--smoke", action="store_true")
    p.add_argument("--out", type=Path, default=None)
    args = p.parse_args()
    started = time.time()

    from sentence_transformers import SentenceTransformer
    import sentence_transformers
    import torch
    import transformers

    ds = beir.load("scifact")
    doc_ids = sorted(ds.corpus)
    train_all, test_qrels = ds.qrels["train"], ds.qrels["test"]
    tr_ids, va_ids = split_queries(list(train_all), VAL_FRACTION, SPLIT_SEED)
    test_ids = sorted(test_qrels)
    if args.smoke:
        tr_ids, va_ids, test_ids = tr_ids[:48], va_ids[:16], test_ids[:16]
        keep = {d for q in tr_ids + va_ids for d in train_all[q]} | {d for q in test_ids for d in test_qrels[q]}
        keep |= set(random.Random(0).sample(doc_ids, 300))
        doc_ids = sorted(keep)
        args.epochs = 1
    train_qrels = {q: train_all[q] for q in tr_ids}
    val_qrels = {q: train_all[q] for q in va_ids}
    test_qrels = {q: test_qrels[q] for q in test_ids}
    assert not set(train_qrels) & set(test_qrels) and not set(val_qrels) & set(train_qrels)
    doc_texts = [ds.doc_text(d) for d in doc_ids]
    doc_text = ds.doc_text
    print(f"corpus {len(doc_ids)} docs; train {len(tr_ids)} / val {len(va_ids)} / test {len(test_ids)} queries", flush=True)

    report: dict = {
        "experiment": "scifact-finetune", "smoke": args.smoke,
        "protocol": __doc__.split("Test labels")[0].strip(),
        "data": {
            "dataset": "BEIR SciFact", "archive": json.loads(beir.LOCK_FILE.read_text())["scifact"],
            "corpus_docs": len(doc_ids), "doc_text": "title + ' ' + text",
            "split_seed": SPLIT_SEED, "val_fraction": VAL_FRACTION,
            "train_queries": len(tr_ids), "val_queries": len(va_ids), "test_queries": len(test_ids),
            "train_pairs": sum(len(v) for v in train_qrels.values()),
            "query_id_digests": {"train": ids_digest(tr_ids), "val": ids_digest(va_ids), "test": ids_digest(test_ids)},
        },
        "environment": {
            "python": platform.python_version(), "platform": platform.platform(), "processor": platform.processor(),
            "torch": torch.__version__, "torch_threads": torch.get_num_threads(),
            "sentence_transformers": sentence_transformers.__version__, "transformers": transformers.__version__,
        },
        "metrics": {}, "validation": {}, "comparisons": {}, "latency_ms": {}, "artifacts": {}, "training": {},
    }

    bm25_started = time.perf_counter()
    bm25 = BM25(doc_texts)
    report["bm25"] = {"k1": bm25.k1, "b": bm25.b, "stemming": False, "index_seconds": time.perf_counter() - bm25_started}
    runs, per = {}, {}
    for split, qrels in (("val", val_qrels), ("test", test_qrels)):
        runs[("bm25", split)] = bm25_run(bm25, ds.queries, sorted(qrels), doc_ids)
    report["validation"]["bm25"] = evaluate(runs[("bm25", "val")], val_qrels, KS)
    report["metrics"]["bm25"] = evaluate(runs[("bm25", "test")], test_qrels, KS)
    per["bm25"] = per_query(runs[("bm25", "test")], test_qrels, "ndcg", 10)
    print("bm25 test", {k: round(v, 4) for k, v in report["metrics"]["bm25"].items()}, flush=True)

    def dense_eval(model, qrels):
        qids = sorted(qrels)
        d = encode(model, doc_texts)
        q = encode(model, [ds.queries[i] for i in qids])
        return dense_run(q, qids, d, doc_ids), d, q

    base_path, base_id = resolve_model(DEFAULT_MODEL)
    pretrained = SentenceTransformer(base_path, device="cpu")
    t0 = time.perf_counter()
    doc_vecs = encode(pretrained, doc_texts)
    report["pretrained_corpus_encode_seconds"] = time.perf_counter() - t0
    val_q = encode(pretrained, [ds.queries[i] for i in sorted(val_qrels)])
    report["validation"]["pretrained"] = evaluate(dense_run(val_q, sorted(val_qrels), doc_vecs, doc_ids), val_qrels, KS)
    test_q = encode(pretrained, [ds.queries[i] for i in test_ids])
    run = dense_run(test_q, test_ids, doc_vecs, doc_ids)
    report["metrics"]["pretrained"] = evaluate(run, test_qrels, KS)
    per["pretrained"] = per_query(run, test_qrels, "ndcg", 10)
    report["latency_ms"]["pretrained"] = query_latency_ms(pretrained, [ds.queries[i] for i in test_ids[:100]])
    report["artifacts"]["pretrained"] = {"model_id": base_id, "bytes": dir_bytes(Path(base_path))}
    if not args.smoke:
        report["artifacts"]["pretrained"]["export"] = export("pretrained", doc_ids, doc_vecs, test_ids, test_q)
    print("pretrained test", {k: round(v, 4) for k, v in report["metrics"]["pretrained"].items()}, flush=True)
    del pretrained

    def validate(model):
        qids = sorted(val_qrels)
        r = dense_run(encode(model, [ds.queries[i] for i in qids]), qids, encode(model, doc_texts), doc_ids)
        return evaluate(r, val_qrels, KS)

    for name in args.configs.split(","):
        cfg = TrainConfig(name=name, epochs=args.epochs, hard_negatives=(name == "hardneg"),
                          batch_size=8 if args.smoke else 32)
        negatives = mine_hard_negatives(bm25, ds.queries, train_qrels, doc_ids, cfg.negative_ranks) if cfg.hard_negatives else {}
        out_dir = ARTIFACTS / ("smoke" if args.smoke else "runs")
        t0 = time.perf_counter()
        result = train(cfg, ds.queries, doc_text, train_qrels, negatives, out_dir, validate,
                       initial=report["validation"]["pretrained"])
        result_dict = asdict(result)
        result_dict["wall_seconds"] = time.perf_counter() - t0
        report["training"][name] = result_dict
        model = SentenceTransformer(result.best_path, device="cpu")
        run, d, q = dense_eval(model, test_qrels)
        report["metrics"][name] = evaluate(run, test_qrels, KS)
        per[name] = per_query(run, test_qrels, "ndcg", 10)
        report["latency_ms"][name] = query_latency_ms(model, [ds.queries[i] for i in test_ids[:100]])
        art = {"selected_epoch": result.best_epoch, "path": result.best_path, "bytes": dir_bytes(Path(result.best_path))}
        if result.best_epoch == 0:
            art["note"] = "no epoch beat the pretrained model on validation; the pretrained weights were selected"
            last = SentenceTransformer(str(out_dir / name / "last"), device="cpu")
            last_run, _, _ = dense_eval(last, test_qrels)
            report["metrics"][name + "_last_epoch_diagnostic"] = evaluate(last_run, test_qrels, KS)
        elif not args.smoke and name == "hardneg":
            art["export"] = export("finetuned-hardneg", doc_ids, d, test_ids, q)
        report["artifacts"][name] = art
        print(f"{name} test", {k: round(v, 4) for k, v in report["metrics"][name].items()}, flush=True)

    pairs = [("bm25", "pretrained")] + [("pretrained", n) for n in args.configs.split(",")]
    if "hardneg" in per and "inbatch" in per:
        pairs.append(("inbatch", "hardneg"))
    for a, b in pairs:
        report["comparisons"][f"{b}_vs_{a}"] = {"metric": "nDCG@10", **paired_bootstrap(per[a], per[b], seed=1)}
    report["wall_seconds"] = time.time() - started

    out = args.out or RESULTS / ("scifact-smoke.json" if args.smoke else "scifact.json")
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2) + "\n")
    print(f"wrote {out} in {report['wall_seconds'] / 60:.1f} min", flush=True)


if __name__ == "__main__":
    main()
