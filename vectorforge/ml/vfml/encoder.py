"""Sentence-embedding encoder with explicit model identity and normalization."""

from __future__ import annotations

import hashlib
import os
import threading
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .preprocess import validate_text

DEFAULT_MODEL = "sentence-transformers/all-MiniLM-L6-v2"
KINDS = ("query", "document")


def _weights_digest(path: Path) -> str:
    """Short SHA-256 of a local model's weight files, used as its version."""
    h = hashlib.sha256()
    files = sorted(p for p in path.rglob("*") if p.suffix in {".safetensors", ".bin"} and p.is_file())
    if not files:
        raise FileNotFoundError(f"no weight files under {path}")
    for p in files:
        with p.open("rb") as f:
            for block in iter(lambda: f.read(1 << 20), b""):
                h.update(block)
    return h.hexdigest()[:12]


def resolve_model(name_or_path: str) -> tuple[str, str]:
    """Return (load_path, model_id) for a Hub name or a local directory.

    Hub models are pinned to the exact cached snapshot revision, so the model
    ID recorded by the database changes if the weights change.
    """
    local = Path(name_or_path)
    if local.is_dir():
        return str(local), f"local/{local.name}@sha256:{_weights_digest(local)}"
    from huggingface_hub import snapshot_download

    try:
        snapshot = snapshot_download(name_or_path, local_files_only=True)
    except Exception:  # not cached yet: download once
        snapshot = snapshot_download(name_or_path)
    revision = Path(snapshot).name
    return snapshot, f"{name_or_path}@{revision[:12]}"


@dataclass(frozen=True)
class Encoded:
    vectors: np.ndarray  # (n, dim) float32, unit L2 norm
    tokens: list[int]  # token count before truncation, including special tokens
    truncated: list[bool]


class Encoder:
    """Loads one Sentence Transformers model on CPU and embeds text.

    Calls are serialized with a lock: PyTorch already parallelizes a batch
    across cores, and interleaving requests would only add contention.
    """

    def __init__(self, model: str = DEFAULT_MODEL, device: str = "cpu", batch_size: int = 32):
        from sentence_transformers import SentenceTransformer

        load_path, self.model_id = resolve_model(model)
        self.name = model
        self.device = device
        self.batch_size = batch_size
        self._model = SentenceTransformer(load_path, device=device)
        self.max_seq_length = int(self._model.max_seq_length)
        dim = getattr(self._model, "get_embedding_dimension", None) or self._model.get_sentence_embedding_dimension
        self.dimension = int(dim())
        self._lock = threading.Lock()

    @property
    def model(self):
        return self._model

    def token_counts(self, texts: list[str]) -> list[int]:
        ids = self._model.tokenizer(texts, add_special_tokens=True, truncation=False)["input_ids"]
        return [len(x) for x in ids]

    def encode(self, texts: list[str], kind: str = "document", batch_size: int | None = None) -> Encoded:
        """Embed texts after validation; returns unit-norm float32 vectors."""
        if kind not in KINDS:
            raise ValueError(f"kind must be one of {KINDS}")
        cleaned = [validate_text(t) for t in texts]
        prompt = kind if kind in getattr(self._model, "prompts", {}) else None
        with self._lock:
            vectors = self._model.encode(
                cleaned,
                batch_size=batch_size or self.batch_size,
                normalize_embeddings=True,
                convert_to_numpy=True,
                show_progress_bar=False,
                prompt_name=prompt,
            )
            tokens = self.token_counts(cleaned)
        vectors = np.asarray(vectors, dtype=np.float32)
        # Re-normalize in float64: the model's Normalize layer works in float32.
        norms = np.linalg.norm(vectors.astype(np.float64), axis=1, keepdims=True)
        if np.any(norms == 0) or not np.all(np.isfinite(vectors)):
            raise RuntimeError("model produced a zero or non-finite embedding")
        vectors = (vectors / norms).astype(np.float32)
        return Encoded(vectors, tokens, [t > self.max_seq_length for t in tokens])


def configure_threads() -> None:
    """Respect VF_TORCH_THREADS so benchmarks can pin CPU usage."""
    value = os.environ.get("VF_TORCH_THREADS")
    if value:
        import torch

        torch.set_num_threads(int(value))
