"""Download, verify and load BEIR-format retrieval datasets.

Archives are pinned by SHA-256 in datasets.lock.json. The first download of a
dataset records its digest (and reports whether the MD5 matches the value
published in the BEIR repository); later loads refuse a different archive.
"""

from __future__ import annotations

import csv
import hashlib
import json
import urllib.request
import zipfile
from dataclasses import dataclass
from pathlib import Path

ML_ROOT = Path(__file__).resolve().parent.parent
DATA_DIR = ML_ROOT / "data"
LOCK_FILE = ML_ROOT / "datasets.lock.json"
BASE_URL = "https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets/{name}.zip"

# MD5 values listed in the BEIR README dataset table.
PUBLISHED_MD5 = {
    "scifact": "5f7d1de60b170fc8027bb7898e2efca1",
    "quora": "18fb154900ba42a600f84b839c173167",
}


@dataclass
class Dataset:
    name: str
    corpus: dict[str, dict[str, str]]  # doc_id -> {"title", "text"}
    queries: dict[str, str]
    qrels: dict[str, dict[str, dict[str, int]]]  # split -> query -> doc -> grade

    def doc_text(self, doc_id: str) -> str:
        """BEIR's convention for bi-encoders: title, a space, then the body."""
        d = self.corpus[doc_id]
        return (d["title"] + " " + d["text"]).strip()


def _digest(path: Path, algo: str) -> str:
    h = hashlib.new(algo)
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def _lock() -> dict:
    return json.loads(LOCK_FILE.read_text()) if LOCK_FILE.exists() else {}


def fetch(name: str) -> Path:
    """Ensure data/<name>/ exists, downloading and verifying the archive."""
    target = DATA_DIR / name
    archive = DATA_DIR / f"{name}.zip"
    lock = _lock()
    if not archive.exists():
        DATA_DIR.mkdir(parents=True, exist_ok=True)
        url = BASE_URL.format(name=name)
        print(f"downloading {url}", flush=True)
        tmp = archive.with_suffix(".part")
        urllib.request.urlretrieve(url, tmp)
        tmp.replace(archive)
    sha = _digest(archive, "sha256")
    if name in lock:
        if lock[name]["sha256"] != sha:
            raise RuntimeError(f"{archive} does not match the pinned SHA-256 in {LOCK_FILE.name}")
    else:
        md5 = _digest(archive, "md5")
        lock[name] = {
            "url": BASE_URL.format(name=name),
            "sha256": sha,
            "md5": md5,
            "md5_matches_beir_readme": PUBLISHED_MD5.get(name) == md5,
            "bytes": archive.stat().st_size,
        }
        LOCK_FILE.write_text(json.dumps(lock, indent=2, sort_keys=True) + "\n")
        print(f"pinned {name}: sha256={sha} md5_matches_beir_readme={lock[name]['md5_matches_beir_readme']}")
    if not (target / "corpus.jsonl").exists():
        with zipfile.ZipFile(archive) as z:
            for member in z.namelist():
                resolved = (DATA_DIR / member).resolve()
                if DATA_DIR.resolve() not in resolved.parents and resolved != DATA_DIR.resolve():
                    raise RuntimeError(f"unsafe path in archive: {member}")
            z.extractall(DATA_DIR)
    return target


def load(name: str) -> Dataset:
    root = fetch(name)
    corpus = {}
    with (root / "corpus.jsonl").open(encoding="utf-8") as f:
        for line in f:
            d = json.loads(line)
            corpus[str(d["_id"])] = {"title": d.get("title", ""), "text": d.get("text", "")}
    queries = {}
    with (root / "queries.jsonl").open(encoding="utf-8") as f:
        for line in f:
            q = json.loads(line)
            queries[str(q["_id"])] = q["text"]
    qrels: dict[str, dict[str, dict[str, int]]] = {}
    for path in sorted((root / "qrels").glob("*.tsv")):
        split: dict[str, dict[str, int]] = {}
        with path.open(encoding="utf-8") as f:
            reader = csv.reader(f, delimiter="\t")
            next(reader)  # header: query-id corpus-id score
            for qid, did, score in reader:
                split.setdefault(qid, {})[did] = int(score)
        qrels[path.stem] = split
    return Dataset(name, corpus, queries, qrels)
