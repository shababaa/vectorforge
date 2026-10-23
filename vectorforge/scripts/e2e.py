#!/usr/bin/env python3
"""End-to-end persistence check against real processes (standard library only).

Builds cmd/vectorforge, creates a scratch corpus by copying a few files of
this repository into a temporary directory, and drives the real server, the
real byodb file and the real embedding service:

  start -> ingest -> search -> kill -> restart -> same search
        -> add / modify / delete files + upload + API delete -> re-ingest
        -> results change as expected -> kill -> restart -> changes persisted

The server is stopped with TerminateProcess/SIGKILL, never gracefully, so a
pass also shows that committed state survives an abrupt stop. The embedding
service must already be running (python -m vfml.service). Nothing outside
the temporary directory is written except the JSON report.

  python vectorforge/scripts/e2e.py --embedder http://127.0.0.1:8090
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
CORPUS_FILES = ["btree.go", "pager.go", "kv.go", "table.go", "codec.go", "docs/DURABILITY.md", "docs/ARCHITECTURE.md"]
QUERIES = [
    "how is a torn meta page detected after a crash",
    "split a B+tree node when it no longer fits in a page",
    "encode a row into a sortable key",
]
NEW_FILE = ("notes/compaction.md", """# Free-list compaction

When most pages of the database file are free, compaction rewrites live
B+tree pages into a new file in key order and atomically renames it over the
old file. Readers keep using the old snapshot until the rename completes.
""")
NEW_QUERY = "compaction rewrites live pages into a new file and renames it"
UPLOAD = ("e2e/raft-notes.md", "# Raft notes\n\nLeader election uses randomized timeouts so that split votes are rare.\n")
UPLOAD_QUERY = "leader election randomized election timeouts split votes"


class Failure(Exception):
    pass


class Client:
    def __init__(self, base: str):
        self.base = base

    def call(self, method: str, path: str, body: dict | None = None, expect: int | None = None) -> tuple[int, dict]:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=300) as resp:
                status, env = resp.status, json.load(resp)
        except urllib.error.HTTPError as exc:
            status, env = exc.code, json.load(exc)
        if expect is not None and status != expect:
            raise Failure(f"{method} {path}: HTTP {status}, expected {expect}: {env}")
        return status, env

    def data(self, method: str, path: str, body: dict | None = None, expect: int = 200) -> dict:
        return self.call(method, path, body, expect)[1]["data"]

    def ingest(self, source: str) -> dict:
        job = self.data("POST", "/api/v1/ingest", {"source": source}, expect=202)
        while job["status"] == "running":
            time.sleep(0.5)
            job = self.data("GET", f"/api/v1/jobs/{job['job_id']}")
        if job["status"] != "succeeded":
            raise Failure(f"ingest {job['status']}: {job.get('error')}")
        return job["result"]

    def search(self, query: str, strategy: str, top_k: int = 5) -> dict:
        return self.data("POST", "/api/v1/search", {"query": query, "top_k": top_k, "strategy": strategy})


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Server:
    def __init__(self, binary: Path, work: Path, embedder: str):
        self.binary, self.work, self.embedder = binary, work, embedder
        self.proc: subprocess.Popen | None = None
        self.port = free_port()
        self.client = Client(f"http://127.0.0.1:{self.port}")
        self.starts = 0

    def start(self) -> float:
        self.starts += 1
        log = open(self.work / f"server-{self.starts}.log", "w")
        args = [str(self.binary), "-db", str(self.work / "e2e.db"), "-sources", str(self.work / "sources.json"),
                "-addr", f"127.0.0.1:{self.port}", "-embedder", self.embedder, "-auto-train-at", "100", "-nprobe", "4"]
        t0 = time.perf_counter()
        self.proc = subprocess.Popen(args, stdout=log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise Failure(f"server exited with {self.proc.returncode}; see {log.name}")
            try:
                if self.client.call("GET", "/health")[0] == 200:
                    return (time.perf_counter() - t0) * 1000
            except (urllib.error.URLError, ConnectionError):
                pass
            time.sleep(0.1)
        raise Failure("server did not become ready")

    def kill(self) -> None:
        if self.proc and self.proc.poll() is None:
            self.proc.kill()  # TerminateProcess on Windows, SIGKILL elsewhere: no graceful shutdown
            self.proc.wait(timeout=30)
        self.proc = None


def snapshot(client: Client) -> dict:
    out = {}
    for q in QUERIES:
        for strategy in ("exact", "ivf"):
            r = client.search(q, strategy)
            out[f"{strategy}:{q}"] = [(h["vector_id"], h["path"], h["start_line"], round(h["score"], 6)) for h in r["results"]]
    return out


def paths(resp: dict) -> list[str]:
    return [h["path"] for h in resp["results"]]


def doc_vectors(client: Client, path: str) -> set[int] | None:
    """Vector IDs of every chunk of the document at path, or None if absent."""
    docs = client.data("GET", f"/api/v1/documents?source=e2e&path_prefix={path}&limit=10")["documents"]
    doc = next((d for d in docs if d["path"] == path), None)
    if doc is None:
        return None
    return {ch["vector_id"] for ch in client.data("GET", f"/api/v1/documents/{doc['doc_id']}")["chunks"]}


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--embedder", default=os.environ.get("VECTORFORGE_EMBEDDER", "http://127.0.0.1:8090"))
    p.add_argument("--binary", help="prebuilt vectorforge binary (default: go build into the temp dir)")
    p.add_argument("--out", default=str(REPO / "vectorforge" / "results" / "e2e" / "e2e.json"))
    p.add_argument("--keep", action="store_true", help="keep the temporary directory")
    args = p.parse_args()

    work = Path(tempfile.mkdtemp(prefix="vectorforge-e2e-"))
    corpus = work / "corpus"
    for rel in CORPUS_FILES:
        (corpus / rel).parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(REPO / rel, corpus / rel)
    (work / "sources.json").write_text(json.dumps({"sources": [{"name": "e2e", "root": "corpus", "description": "copies of repo files"}]}))
    binary = Path(args.binary) if args.binary else work / ("vectorforge.exe" if os.name == "nt" else "vectorforge")
    if not args.binary:
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/vectorforge"], cwd=REPO, check=True)

    steps: list[dict] = []
    report = {"started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "platform": platform.platform(),
              "corpus_files": CORPUS_FILES, "queries": QUERIES, "steps": steps, "passed": False}

    def check(name: str, ok: bool, **detail) -> None:
        steps.append({"step": name, "ok": bool(ok), **detail})
        print(f"[{'PASS' if ok else 'FAIL'}] {name}" + (f"  {detail}" if detail else ""), flush=True)
        if not ok:
            raise Failure(name)

    server = Server(binary, work, args.embedder)
    c = server.client
    try:
        ms = server.start()
        health = c.data("GET", "/health")
        check("start on an empty database", health["vectors"] == 0, startup_ms=round(ms), model=health["model_id"])

        first = c.ingest("e2e")
        check("ingest corpus", first["added"] == len(CORPUS_FILES) and first["failed"] == 0,
              added=first["added"], chunks=first["chunks_embedded"], embed_ms=first["embed_millis"], ivf_trained=first["auto_trained"])
        stats = c.data("GET", "/api/v1/index/stats")
        check("IVF trained during ingest", stats["ivf"]["trained"], nlist=stats["ivf"]["meta"]["nlist"], vectors=stats["vectors"])

        before = snapshot(c)
        top = {q: before[f"exact:{q}"][0][1] for q in QUERIES}
        check("searches return results", all(before.values()), top_exact_hits=top)

        again = c.ingest("e2e")
        check("re-ingest is a no-op", again["unchanged"] == len(CORPUS_FILES) and again["chunks_embedded"] == 0,
              unchanged=again["unchanged"], chunks_embedded=again["chunks_embedded"])

        server.kill()
        ms = server.start()
        after = snapshot(c)
        startup = c.data("GET", "/api/v1/index/stats")["startup"]
        check("restart after kill: IVF loaded, not retrained", startup["ivf_loaded"] and not startup["ivf_retrained"],
              startup_ms=round(ms), load_ms=startup["load_millis"], vectors=startup["vectors"])
        check("identical results after restart (IDs, ranks, scores)", after == before)
        check("consistency after restart", c.data("GET", "/api/v1/index/verify")["ok"])

        # Mutations: add, modify and delete files, upload and delete via the API.
        codec_before = doc_vectors(c, "codec.go")
        (corpus / NEW_FILE[0]).parent.mkdir(parents=True, exist_ok=True)
        (corpus / NEW_FILE[0]).write_text(NEW_FILE[1])
        (corpus / "docs/ARCHITECTURE.md").unlink()
        codec = corpus / "codec.go"
        codec.write_text(codec.read_text() + "\n// e2e: appended comment changes the content hash\n")
        mutated = c.ingest("e2e")
        check("re-ingest after file changes", (mutated["added"], mutated["updated"], mutated["deleted"]) == (1, 1, 1),
              added=mutated["added"], updated=mutated["updated"], deleted=mutated["deleted"], unchanged=mutated["unchanged"])
        hit = c.search(NEW_QUERY, "exact", 3)
        check("new file is searchable", paths(hit)[0] == NEW_FILE[0], top=paths(hit))
        gone = [h for r in (c.search(q, "exact", 20) for q in QUERIES) for h in r["results"] if h["path"] == "docs/ARCHITECTURE.md"]
        check("deleted file no longer listed or returned", not gone and doc_vectors(c, "docs/ARCHITECTURE.md") is None)
        codec_after = doc_vectors(c, "codec.go")
        check("modified file re-embedded with fresh vector IDs", bool(codec_before) and bool(codec_after) and not (codec_before & codec_after),
              old_chunks=len(codec_before or ()), new_chunks=len(codec_after or ()))

        up = c.data("POST", "/api/v1/documents", {"path": UPLOAD[0], "content": UPLOAD[1]}, expect=201)["document"]
        hit = c.search(UPLOAD_QUERY, "exact", 3)
        check("uploaded document is searchable", paths(hit)[0] == UPLOAD[0], top=paths(hit))
        ivf_hit = c.search(UPLOAD_QUERY, "ivf", 3)
        steps.append({"step": "uploaded document via IVF (informational)", "ok": True, "top": paths(ivf_hit),
                      "note": "off-distribution inserts can land in lists the query does not probe"})
        c.data("DELETE", f"/api/v1/documents/{up['doc_id']}")
        c.call("GET", f"/api/v1/documents/{up['doc_id']}", expect=404)
        hit = c.search(UPLOAD_QUERY, "exact", 3)
        check("API delete removes the document from results", UPLOAD[0] not in paths(hit), top=paths(hit))
        expected = snapshot(c)
        check("consistency after mutations", c.data("GET", "/api/v1/index/verify")["ok"])

        server.kill()
        server.start()
        check("mutations persisted across a second kill/restart", snapshot(c) == expected)
        final = c.data("GET", "/api/v1/index/verify")
        check("final consistency", final["ok"], documents=final["documents"], vectors=final["vectors"], postings=final["postings"])
        report["passed"] = True
    except Failure as exc:
        report["error"] = str(exc)
        print(f"e2e failed: {exc}; server logs in {work}", file=sys.stderr)
        args.keep = True
    finally:
        server.kill()
        report["finished_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        out = Path(args.out)
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(json.dumps(report, indent=2) + "\n")
        print(f"wrote {out}")
        if not args.keep:
            shutil.rmtree(work, ignore_errors=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
