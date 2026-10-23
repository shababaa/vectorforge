"""End-to-end search latency through the HTTP API.

  python vectorforge/scripts/apibench.py --url http://127.0.0.1:8080

Sends the 44 evaluation queries (vectorforge/eval/codesearch.json) to
POST /api/v1/search for each strategy, serially and with concurrent clients,
and records client wall time next to the server's own breakdown (embed,
index search, chunk fetch). Each worker keeps one persistent HTTP connection.
Run it on an otherwise idle machine; the embedding service runs on the same
CPU as the database.
"""

from __future__ import annotations

import argparse
import http.client
import json
import math
import os
import platform
import threading
import time
import urllib.parse
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def pct(values: list[float], p: float) -> float:
    s = sorted(values)
    return s[max(0, math.ceil(p / 100 * len(s)) - 1)] if s else 0.0


def summary(values: list[float]) -> dict:
    return {"n": len(values), "mean": sum(values) / max(1, len(values)), "p50": pct(values, 50),
            "p95": pct(values, 95), "p99": pct(values, 99), "max": max(values, default=0.0)}


class Client:
    def __init__(self, url: str):
        u = urllib.parse.urlsplit(url)
        self.conn = http.client.HTTPConnection(u.hostname, u.port or 80, timeout=60)
        self.headers = {"Content-Type": "application/json"}
        if token := os.environ.get("VECTORFORGE_API_TOKEN"):
            self.headers["Authorization"] = "Bearer " + token

    def call(self, method: str, path: str, body: dict | None = None) -> dict:
        self.conn.request(method, path, json.dumps(body) if body is not None else None, self.headers)
        resp = self.conn.getresponse()
        data = resp.read()
        if resp.status != 200:
            raise RuntimeError(f"{method} {path}: HTTP {resp.status} {data[:200]!r}")
        return json.loads(data)["data"]


def search(c: Client, query: str, strategy: str, nprobe: int) -> tuple[float, dict]:
    body = {"query": query, "top_k": 10, "strategy": strategy, "include_text": False}
    if strategy == "ivf" and nprobe:
        body["nprobe"] = nprobe
    t0 = time.perf_counter()
    resp = c.call("POST", "/api/v1/search", body)
    return (time.perf_counter() - t0) * 1000, resp["timing"]


def serial(url: str, queries: list[str], strategy: str, nprobe: int, repeats: int) -> dict:
    c = Client(url)
    for q in queries:  # warm-up: connection, caches, embedding model
        search(c, q, strategy, nprobe)
    wall, parts = [], {"embed_ms": [], "search_ms": [], "fetch_ms": [], "total_ms": []}
    for _ in range(repeats):
        for q in queries:
            ms, timing = search(c, q, strategy, nprobe)
            wall.append(ms)
            for k in parts:
                parts[k].append(timing[k])
    return {"strategy": strategy, "nprobe": nprobe if strategy == "ivf" else None,
            "client_wall_ms": summary(wall), "server_ms": {k: summary(v) for k, v in parts.items()}}


def concurrent(url: str, queries: list[str], strategy: str, nprobe: int, workers: int, total: int) -> dict:
    lock, next_i, lat, errors = threading.Lock(), [0], [], []

    def worker():
        c = Client(url)
        search(c, queries[0], strategy, nprobe)
        while True:
            with lock:
                i = next_i[0]
                next_i[0] += 1
            if i >= total:
                return
            try:
                ms, _ = search(c, queries[i % len(queries)], strategy, nprobe)
                with lock:
                    lat.append(ms)
            except Exception as exc:  # recorded, not hidden
                with lock:
                    errors.append(str(exc))

    threads = [threading.Thread(target=worker) for _ in range(workers)]
    t0 = time.perf_counter()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    elapsed = time.perf_counter() - t0
    return {"strategy": strategy, "nprobe": nprobe if strategy == "ivf" else None, "workers": workers,
            "requests": len(lat), "errors": len(errors), "qps": len(lat) / elapsed, "client_wall_ms": summary(lat)}


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--url", default="http://127.0.0.1:8080")
    p.add_argument("--queries", type=Path, default=ROOT / "vectorforge/eval/codesearch.json")
    p.add_argument("--repeats", type=int, default=5)
    p.add_argument("--workers", default="1,2,4,8")
    p.add_argument("--total", type=int, default=440, help="requests per concurrency level")
    p.add_argument("--out", type=Path, default=ROOT / "vectorforge/results/bench/api.json")
    p.add_argument("--label", default="", help="describes the configuration under test, e.g. embedder thread count")
    args = p.parse_args()

    queries = [q["query"] for q in json.loads(args.queries.read_text())["queries"]]
    c = Client(args.url)
    stats = c.call("GET", "/api/v1/index/stats")
    health = c.call("GET", "/health")
    nprobe = stats.get("ivf", {}).get("default_nprobe") or 8
    report = {
        "label": args.label,
        "measured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "methodology": __doc__.strip(),
        "environment": {"python": platform.python_version(), "platform": platform.platform(),
                        "cpu_count": os.cpu_count()},
        "server": {"health": health, "vectors": stats.get("vectors"), "documents": stats.get("documents"),
                   "ivf": {k: v for k, v in stats.get("ivf", {}).items() if not isinstance(v, (list, dict))}},
        "queries": len(queries), "repeats": args.repeats, "serial": [], "concurrent": [],
    }
    for strategy in ("exact", "ivf"):
        r = serial(args.url, queries, strategy, nprobe, args.repeats)
        report["serial"].append(r)
        s = r["server_ms"]
        print(f"serial {strategy:5s} wall p50 {r['client_wall_ms']['p50']:.2f} ms  embed {s['embed_ms']['p50']:.2f}  "
              f"search {s['search_ms']['p50']:.3f}  fetch {s['fetch_ms']['p50']:.3f}", flush=True)
    for strategy in ("exact", "ivf"):
        for w in (int(x) for x in args.workers.split(",")):
            r = concurrent(args.url, queries, strategy, nprobe, w, args.total)
            report["concurrent"].append(r)
            print(f"{strategy:5s} workers={w}: {r['qps']:.1f} QPS, p50 {r['client_wall_ms']['p50']:.1f} ms, "
                  f"p99 {r['client_wall_ms']['p99']:.1f} ms, errors {r['errors']}", flush=True)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    print(f"wrote {args.out}")


if __name__ == "__main__":
    main()
