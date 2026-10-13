#!/usr/bin/env python3
"""Command-line client for the VectorForge HTTP API (standard library only).

  python vectorforge/scripts/vfctl.py ingest byodb
  python vectorforge/scripts/vfctl.py search "how are torn commits detected" --top-k 5
  python vectorforge/scripts/vfctl.py rebuild --nlist 64
  python vectorforge/scripts/vfctl.py stats | verify | runs

Set VECTORFORGE_URL (default http://127.0.0.1:8080) and, if the server
requires one, VECTORFORGE_API_TOKEN.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("VECTORFORGE_URL", "http://127.0.0.1:8080").rstrip("/")


def call(method: str, path: str, body: dict | None = None) -> dict:
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token := os.environ.get("VECTORFORGE_API_TOKEN"):
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=300) as resp:
            return json.load(resp)["data"]
    except urllib.error.HTTPError as exc:
        err = json.load(exc).get("error", {})
        raise SystemExit(f"HTTP {exc.code} {err.get('code')}: {err.get('message')}") from None


def wait_job(job_id: str, quiet: bool = False) -> dict:
    last = None
    while True:
        job = call("GET", f"/api/v1/jobs/{job_id}")
        progress = job.get("progress", {})
        line = f"{job['status']}: files {progress.get('files_done')}/{progress.get('files_total')}, chunks {progress.get('chunks_embedded')}"
        if not quiet and line != last:
            print(line, file=sys.stderr)
            last = line
        if job["status"] != "running":
            return job
        time.sleep(1)


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    ing = sub.add_parser("ingest")
    ing.add_argument("source")
    ing.add_argument("--force", action="store_true")
    s = sub.add_parser("search")
    s.add_argument("query")
    s.add_argument("--top-k", type=int, default=5)
    s.add_argument("--strategy", default="auto", choices=["auto", "exact", "ivf"])
    s.add_argument("--nprobe", type=int, default=0)
    s.add_argument("--kind", action="append")
    s.add_argument("--source", action="append")
    s.add_argument("--path-prefix", default="")
    s.add_argument("--json", action="store_true")
    r = sub.add_parser("rebuild")
    r.add_argument("--nlist", type=int, default=0)
    r.add_argument("--seed", type=int, default=0)
    for name in ("stats", "verify", "runs", "health"):
        sub.add_parser(name)
    args = p.parse_args()

    if args.cmd == "ingest":
        job = call("POST", "/api/v1/ingest", {"source": args.source, "force": args.force})
        print(json.dumps(wait_job(job["job_id"]), indent=2))
    elif args.cmd == "search":
        body = {"query": args.query, "top_k": args.top_k, "strategy": args.strategy, "nprobe": args.nprobe,
                "filters": {"kinds": args.kind or [], "sources": args.source or [], "path_prefix": args.path_prefix}}
        resp = call("POST", "/api/v1/search", body)
        if args.json:
            print(json.dumps(resp, indent=2))
            return
        t = resp["timing"]
        print(f"{resp['strategy']} over {resp['vectors_indexed']} vectors"
              + (f" (nprobe {resp['nprobe']}/{resp['nlist']})" if resp.get("nprobe") else "")
              + f": embed {t['embed_ms']:.1f} ms, search {t['search_ms']:.2f} ms, fetch {t['fetch_ms']:.2f} ms")
        for h in resp["results"]:
            print(f"\n{h['rank']:2d}. {h['score']:.3f}  {h['source']}:{h['path']}:{h['start_line']}-{h['end_line']}  [{h['section']}]")
            print("    " + h["snippet"].replace("\n", "\n    ")[:400])
    elif args.cmd == "rebuild":
        job = call("POST", "/api/v1/index/rebuild", {k: v for k, v in {"nlist": args.nlist, "seed": args.seed}.items() if v})
        print(json.dumps(wait_job(job["job_id"]), indent=2))
    elif args.cmd == "stats":
        print(json.dumps(call("GET", "/api/v1/index/stats"), indent=2))
    elif args.cmd == "verify":
        print(json.dumps(call("GET", "/api/v1/index/verify"), indent=2))
    elif args.cmd == "runs":
        print(json.dumps(call("GET", "/api/v1/runs"), indent=2))
    elif args.cmd == "health":
        req = urllib.request.urlopen(BASE + "/health", timeout=10)
        print(json.dumps(json.load(req)["data"], indent=2))


if __name__ == "__main__":
    main()
