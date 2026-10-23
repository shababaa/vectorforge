import json

from vfml.annrelevance import ann_recall, load_runs


def test_ann_recall_against_exact_run():
    exact = {"q1": ["a", "b", "c", "d"], "q2": ["x", "y"]}
    approx = {"q1": ["a", "c", "e", "f"], "q2": ["y", "x"]}
    # q1 keeps 2 of its exact top 4, q2 keeps both: (0.5 + 1) / 2
    assert ann_recall(approx, exact, 4) == 0.75
    assert ann_recall({}, exact, 4) == 0.0


def test_load_runs_groups_by_corpus_and_model(tmp_path):
    def write(name, **fields):
        (tmp_path / name).write_text(json.dumps({"runs": {}, **fields}))

    write("a.json", corpus="scifact", model="pretrained", strategy="exact")
    write("b.json", corpus="scifact", model="pretrained", strategy="ivf", nprobe=4)
    write("c.json", corpus="quora", model="pretrained", strategy="exact")
    write("old.json", dataset="synthetic", strategy="exact")  # no corpus: not a real-data run
    groups = load_runs(tmp_path)
    assert sorted(groups) == [("quora", "pretrained"), ("scifact", "pretrained")]
    assert [f["file"] for f in groups[("scifact", "pretrained")]] == ["a.json", "b.json"]
