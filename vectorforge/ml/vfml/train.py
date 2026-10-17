"""Contrastive fine-tuning of a bi-encoder with in-batch and BM25 hard negatives.

For a batch of B triples (query q_i, positive p_i, hard negative n_i) the
candidate set is C = [p_1..p_B, n_1..n_B] and

    s_ij = scale * cos(q_i, c_j)
    L    = mean_i  -log( exp(s_ii) / sum_j exp(s_ij) )

which is the InfoNCE / multiple-negatives ranking loss: every other query's
positive and every hard negative act as negatives for q_i. scale = 1/T is the
inverse temperature. Candidates that are labelled positives of q_i (other
than its own target) are masked, so a document shared by two queries in the
batch is never pushed away from a query it is relevant to.

The loop is written out explicitly (rather than using a trainer class) so
every step is visible: tokenization, forward pass, similarity matrix, masked
cross-entropy, gradient clipping, AdamW with linear warmup and decay.
"""

from __future__ import annotations

import math
import random
import time
from collections.abc import Callable
from dataclasses import asdict, dataclass, field
from pathlib import Path

import numpy as np
import torch
import torch.nn.functional as F

from .bm25 import BM25
from .encoder import DEFAULT_MODEL, resolve_model
from .preprocess import normalize_text


@dataclass
class TrainConfig:
    name: str = "hardneg"
    base_model: str = DEFAULT_MODEL
    epochs: int = 3
    batch_size: int = 32
    lr: float = 2e-5
    warmup_ratio: float = 0.1
    weight_decay: float = 0.01
    scale: float = 20.0  # inverse temperature; T = 0.05
    max_seq_length: int = 256
    hard_negatives: bool = True
    # Sample each hard negative from BM25 ranks [lo, hi) after removing the
    # query's labelled positives. Skipping the very top reduces the chance of
    # picking an unlabelled relevant document (a false negative).
    negative_ranks: tuple[int, int] = (3, 30)
    grad_clip: float = 1.0
    seed: int = 13


@dataclass
class EpochLog:
    epoch: int
    steps: int
    mean_loss: float | None
    train_seconds: float
    validation: dict[str, float]
    selected: bool = False


@dataclass
class TrainResult:
    config: dict
    history: list[EpochLog] = field(default_factory=list)
    best_epoch: int = 0
    best_path: str = ""
    examples_per_epoch: int = 0
    queries_with_negatives: int = 0


def mine_hard_negatives(bm25: BM25, queries: dict[str, str], qrels: dict[str, dict[str, int]],
                        doc_ids: list[str], ranks: tuple[int, int]) -> dict[str, list[str]]:
    """BM25 candidates per training query, excluding its labelled positives."""
    lo, hi = ranks
    out = {}
    for qid, rel in qrels.items():
        ranked = [doc_ids[i] for i, _ in bm25.search(queries[qid], hi + len(rel))]
        negatives = [d for d in ranked if rel.get(d, 0) <= 0][lo:hi]
        if negatives:
            out[qid] = negatives
    return out


def build_examples(qrels: dict[str, dict[str, int]], negatives: dict[str, list[str]],
                   rng: random.Random, use_negatives: bool) -> list[tuple[str, str, str | None]]:
    """One (query, positive, negative) triple per labelled pair; negatives are
    re-sampled every epoch for diversity."""
    examples = []
    for qid in sorted(qrels):
        for did in sorted(d for d, g in qrels[qid].items() if g > 0):
            neg = rng.choice(negatives[qid]) if use_negatives and qid in negatives else None
            examples.append((qid, did, neg))
    rng.shuffle(examples)
    return examples


def _embed(model, texts: list[str]) -> torch.Tensor:
    prep = getattr(model, "preprocess", None) or model.tokenize
    features = prep([normalize_text(t) or " " for t in texts])
    return F.normalize(model(features)["sentence_embedding"], dim=-1)


def contrastive_loss(q: torch.Tensor, c: torch.Tensor, scale: float, mask: torch.Tensor) -> torch.Tensor:
    """Masked InfoNCE. q: (B, d); c: (M, d) with c[i] the target of q[i];
    mask[i, j] is True where candidate j must be ignored for query i."""
    scores = scale * (q @ c.T)
    scores = scores.masked_fill(mask, float("-inf"))
    return F.cross_entropy(scores, torch.arange(q.shape[0]))


def train(cfg: TrainConfig, queries: dict[str, str], doc_text: Callable[[str], str],
          train_qrels: dict[str, dict[str, int]], negatives: dict[str, list[str]], out_dir: Path,
          validate: Callable[[object], dict[str, float]], select_metric: str = "nDCG@10",
          initial: dict[str, float] | None = None) -> TrainResult:
    """Fine-tune and keep the epoch with the best validation metric.

    initial is the base model's validation result, if already computed."""
    from sentence_transformers import SentenceTransformer

    random.seed(cfg.seed)
    np.random.seed(cfg.seed)
    torch.manual_seed(cfg.seed)
    rng = random.Random(cfg.seed)
    path, _ = resolve_model(cfg.base_model)
    model = SentenceTransformer(path, device="cpu")
    model.max_seq_length = cfg.max_seq_length

    result = TrainResult(config=asdict(cfg), queries_with_negatives=len(negatives))
    model.eval()
    base = initial if initial is not None else validate(model)
    result.history.append(EpochLog(0, 0, None, 0.0, base))
    best, best_epoch = base[select_metric], 0
    best_dir = out_dir / cfg.name / "best"
    print(f"[{cfg.name}] epoch 0 (pretrained) {select_metric}={best:.4f}", flush=True)

    n_examples = len(build_examples(train_qrels, negatives, random.Random(0), cfg.hard_negatives))
    steps_per_epoch = math.ceil(n_examples / cfg.batch_size)
    total_steps = steps_per_epoch * cfg.epochs
    warmup = max(1, int(cfg.warmup_ratio * total_steps))
    result.examples_per_epoch = n_examples
    optimizer = torch.optim.AdamW(model.parameters(), lr=cfg.lr, weight_decay=cfg.weight_decay)
    scheduler = torch.optim.lr_scheduler.LambdaLR(
        optimizer, lambda s: (s + 1) / warmup if s < warmup else max(0.0, (total_steps - s) / max(1, total_steps - warmup)))

    step = 0
    for epoch in range(1, cfg.epochs + 1):
        examples = build_examples(train_qrels, negatives, rng, cfg.hard_negatives)
        model.train()
        started, losses = time.perf_counter(), []
        for start in range(0, len(examples), cfg.batch_size):
            batch = examples[start:start + cfg.batch_size]
            cand_ids = [p for _, p, _ in batch] + [n for _, _, n in batch if n is not None]
            q = _embed(model, [queries[qid] for qid, _, _ in batch])
            c = _embed(model, [doc_text(d) for d in cand_ids])
            mask = torch.zeros(len(batch), len(cand_ids), dtype=torch.bool)
            for i, (qid, _, _) in enumerate(batch):
                rel = train_qrels[qid]
                for j, did in enumerate(cand_ids):
                    if j != i and rel.get(did, 0) > 0:
                        mask[i, j] = True
            loss = contrastive_loss(q, c, cfg.scale, mask)
            optimizer.zero_grad(set_to_none=True)
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), cfg.grad_clip)
            optimizer.step()
            scheduler.step()
            losses.append(float(loss.detach()))
            step += 1
            if step % 10 == 0:
                print(f"[{cfg.name}] epoch {epoch} step {step}/{total_steps} loss {np.mean(losses[-10:]):.4f}", flush=True)
        train_seconds = time.perf_counter() - started
        model.eval()
        metrics = validate(model)
        log = EpochLog(epoch, len(losses), float(np.mean(losses)), train_seconds, metrics)
        improved = metrics[select_metric] > best
        if improved:
            best, best_epoch = metrics[select_metric], epoch
            model.save(str(best_dir))
        result.history.append(log)
        print(f"[{cfg.name}] epoch {epoch} loss {log.mean_loss:.4f} {select_metric}={metrics[select_metric]:.4f} "
              f"({train_seconds:.0f}s){' *' if improved else ''}", flush=True)
    model.save(str(out_dir / cfg.name / "last"))
    for log in result.history:
        log.selected = log.epoch == best_epoch
    result.best_epoch = best_epoch
    result.best_path = str(best_dir) if best_epoch > 0 else path
    return result
