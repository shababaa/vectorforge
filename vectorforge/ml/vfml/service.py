"""FastAPI embedding service used by the Go search engine.

Run:  python -m vfml.service --model sentence-transformers/all-MiniLM-L6-v2 --port 8090

Endpoints
  GET  /health       model identity, dimension and limits
  POST /embed        {"text": str, "kind": "query"|"document"}
  POST /embed/batch  {"texts": [str, ...], "kind": "query"|"document"}

Every vector is L2-normalized float32 serialized as JSON numbers. Inputs are
validated (non-empty after normalization, at most MAX_TEXT_CHARS characters,
at most MAX_BATCH texts); invalid input returns HTTP 422.
"""

from __future__ import annotations

import argparse
import time
from typing import Literal

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, ConfigDict, Field

from .encoder import DEFAULT_MODEL, Encoder, configure_threads
from .preprocess import MAX_TEXT_CHARS, TextError

MAX_BATCH = 256


class EmbedRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    text: str = Field(min_length=1, max_length=MAX_TEXT_CHARS * 2)
    kind: Literal["query", "document"] = "query"


class BatchRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    texts: list[str] = Field(min_length=1, max_length=MAX_BATCH)
    kind: Literal["query", "document"] = "document"


def create_app(encoder: Encoder) -> FastAPI:
    app = FastAPI(title="VectorForge embedding service", version="1.0.0")

    def info() -> dict:
        return {"model_id": encoder.model_id, "dimension": encoder.dimension}

    @app.get("/health")
    def health() -> dict:
        return {
            "status": "ok",
            **info(),
            "model_name": encoder.name,
            "max_seq_length": encoder.max_seq_length,
            "normalized": True,
            "metric": "cosine",
            "device": encoder.device,
            "max_batch": MAX_BATCH,
            "max_text_chars": MAX_TEXT_CHARS,
        }

    def run(texts: list[str], kind: str):
        started = time.perf_counter()
        try:
            encoded = encoder.encode(texts, kind=kind)
        except TextError as exc:
            raise HTTPException(status_code=422, detail=str(exc)) from exc
        return encoded, round((time.perf_counter() - started) * 1000, 3)

    @app.post("/embed")
    def embed(request: EmbedRequest) -> dict:
        encoded, elapsed = run([request.text], request.kind)
        return {
            **info(),
            "embedding": encoded.vectors[0].tolist(),
            "tokens": encoded.tokens[0],
            "truncated": encoded.truncated[0],
            "elapsed_ms": elapsed,
        }

    @app.post("/embed/batch")
    def embed_batch(request: BatchRequest) -> dict:
        encoded, elapsed = run(request.texts, request.kind)
        return {
            **info(),
            "embeddings": encoded.vectors.tolist(),
            "tokens": encoded.tokens,
            "truncated": encoded.truncated,
            "elapsed_ms": elapsed,
        }

    return app


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--model", default=DEFAULT_MODEL, help="Hub model name or local model directory")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8090)
    parser.add_argument("--batch-size", type=int, default=32)
    args = parser.parse_args()
    configure_threads()
    import uvicorn

    encoder = Encoder(args.model, batch_size=args.batch_size)
    print(f"loaded {encoder.model_id} ({encoder.dimension}-d, max {encoder.max_seq_length} tokens)", flush=True)
    uvicorn.run(create_app(encoder), host=args.host, port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
