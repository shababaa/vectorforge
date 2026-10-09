"""Service tests against the real all-MiniLM-L6-v2 model on CPU."""

import numpy as np
import pytest
from fastapi.testclient import TestClient

from vfml.encoder import DEFAULT_MODEL, Encoder
from vfml.service import MAX_BATCH, create_app


@pytest.fixture(scope="module")
def encoder():
    try:
        return Encoder(DEFAULT_MODEL)
    except Exception as exc:  # pragma: no cover - only without network and cache
        pytest.skip(f"model unavailable: {exc}")


@pytest.fixture(scope="module")
def client(encoder):
    return TestClient(create_app(encoder))


def test_health_reports_model_identity(client, encoder):
    body = client.get("/health").json()
    assert body["status"] == "ok"
    assert body["dimension"] == 384
    assert body["model_id"].startswith(DEFAULT_MODEL + "@")
    assert len(body["model_id"].split("@")[1]) == 12
    assert body["normalized"] is True
    assert body["model_id"] == encoder.model_id


def test_embed_returns_unit_vectors(client):
    body = client.post("/embed", json={"text": "copy-on-write B+tree", "kind": "query"}).json()
    v = np.asarray(body["embedding"], dtype=np.float64)
    assert v.shape == (384,)
    assert abs(np.linalg.norm(v) - 1) < 1e-5
    assert body["tokens"] > 2 and body["truncated"] is False


def test_batch_matches_single_and_is_deterministic(client):
    texts = ["how are pages freed", "snapshot isolation for readers", "how are pages freed"]
    batch = client.post("/embed/batch", json={"texts": texts}).json()
    vs = np.asarray(batch["embeddings"], dtype=np.float32)
    assert vs.shape == (3, 384)
    np.testing.assert_allclose(vs[0], vs[2], atol=1e-6)
    single = np.asarray(client.post("/embed", json={"text": texts[1], "kind": "document"}).json()["embedding"])
    np.testing.assert_allclose(vs[1], single, atol=1e-5)


def test_semantics_sanity(encoder):
    e = encoder.encode(
        ["How does the database survive a crash?", "durable commit with fsync and meta pages", "banana bread recipe"],
        kind="document",
    ).vectors
    assert e[0] @ e[1] > e[0] @ e[2]


def test_truncation_is_reported(client, encoder):
    long_text = "word " * (encoder.max_seq_length + 50)
    body = client.post("/embed", json={"text": long_text}).json()
    assert body["truncated"] is True
    assert body["tokens"] > encoder.max_seq_length


@pytest.mark.parametrize(
    "payload",
    [
        {"text": ""},
        {"text": "   \x00  "},
        {"text": "x", "kind": "image"},
        {"text": "x", "extra": 1},
        {},
    ],
)
def test_embed_rejects_invalid_input(client, payload):
    assert client.post("/embed", json=payload).status_code == 422


def test_batch_limits(client):
    assert client.post("/embed/batch", json={"texts": []}).status_code == 422
    assert client.post("/embed/batch", json={"texts": ["a"] * (MAX_BATCH + 1)}).status_code == 422
    assert client.post("/embed/batch", json={"texts": ["fine", "  "]}).status_code == 422
