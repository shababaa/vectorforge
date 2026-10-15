import math

import pytest

from vfml.bm25 import BM25, tokenize
from vfml.metrics import dedupe, evaluate, ndcg, recall, reciprocal_rank
from vfml.preprocess import MAX_TEXT_CHARS, TextError, normalize_text, validate_text


def test_normalize_text_is_conservative():
    assert normalize_text("  caf\u0065\u0301\r\n\tx\x00y  ") == "caf\u00e9\n\txy"
    code = "def f():\n    return 1"
    assert normalize_text(code) == code


def test_validate_text_rejects_empty_and_oversized():
    with pytest.raises(TextError):
        validate_text(" \n\x07 ")
    with pytest.raises(TextError):
        validate_text("a" * (MAX_TEXT_CHARS + 1))
    assert validate_text("a" * MAX_TEXT_CHARS) == "a" * MAX_TEXT_CHARS


def test_reciprocal_rank_and_recall_by_hand():
    rel = {"d3": 1, "d7": 2}
    ranked = ["d1", "d3", "d2", "d7"]
    assert reciprocal_rank(ranked, rel, 10) == 0.5
    assert reciprocal_rank(ranked, rel, 1) == 0.0
    assert recall(ranked, rel, 2) == 0.5
    assert recall(ranked, rel, 4) == 1.0
    with pytest.raises(ValueError):
        recall(ranked, {"d1": 0}, 10)


def test_ndcg_by_hand():
    rel = {"a": 2, "b": 1}
    # DCG = 1/log2(2) + 2/log2(3); IDCG = 2/log2(2) + 1/log2(3)
    got = ndcg(["b", "a", "c"], rel, 10)
    want = (1 + 2 / math.log2(3)) / (2 + 1 / math.log2(3))
    assert got == pytest.approx(want)
    assert ndcg(["a", "b"], rel, 10) == pytest.approx(1.0)
    assert ndcg(["x", "y"], rel, 10) == 0.0


def test_evaluate_macro_averages_and_skips_unjudged():
    qrels = {"q1": {"a": 1}, "q2": {"b": 1}, "q3": {"c": 0}}
    run = {"q1": ["a"], "q2": ["x", "b"]}
    m = evaluate(run, qrels, ks=(1, 10))
    assert m["queries"] == 2
    assert m["MRR@10"] == pytest.approx((1 + 0.5) / 2)
    assert m["Recall@1"] == pytest.approx(0.5)
    assert m["P@1"] == pytest.approx(0.5)
    missing = evaluate({}, qrels, ks=(10,))
    assert missing["MRR@10"] == 0.0


def test_dedupe_keeps_first():
    assert dedupe(["a", "b", "a", "c", "b"]) == ["a", "b", "c"]


def test_tokenize_splits_identifiers_only_when_asked():
    assert tokenize("The seekLE of delete_range") == ["seekle", "delete", "range"]
    split = tokenize("seekLE delete_range", split_identifiers=True)
    assert {"seekle", "seek", "le", "delete", "range"} <= set(split)


def test_bm25_ranks_rare_term_matches_first():
    docs = [
        "the cat sat on the mat",
        "copy on write b tree pages",
        "a tree of cats",
        "write ahead log versus copy on write",
    ]
    bm = BM25(docs)
    hits = bm.search("copy-on-write tree", 4)
    assert hits[0][0] == 1
    assert {i for i, _ in hits} == {1, 2, 3}
    assert bm.search("unseen words", 3) == []
    assert all(hits[i][1] >= hits[i + 1][1] for i in range(len(hits) - 1))


def test_bm25_idf_and_length_normalization():
    bm = BM25(["x y", "x z", "x"], k1=1.2, b=0.75)
    assert bm.idf("x") < bm.idf("y")
    s = bm.scores("x")
    assert s[2] > s[0]  # shorter document wins for the same term frequency
