# VectorForge interview guide

Short, defensible answers to the questions this project invites, each with a
pointer to the code or measurement that backs it. Numbers are from
`vectorforge/results/`; see [benchmarks.md](benchmarks.md) and
[ml-methodology.md](ml-methodology.md) for how they were produced. If a claim
here is not backed by a file in the repository, it is labelled as reasoning.

---

### 1. Give me the 30-second version.

VectorForge is a semantic search engine I built on top of my own database.
Files are split into structure-aware chunks, embedded by a local MiniLM model
behind a FastAPI service, and stored transactionally in byodb, a
copy-on-write B+tree engine I wrote earlier. Search is either exact (a scan
of every vector) or IVF-Flat, an approximate index I implemented in Go:
spherical k-means clusters the vectors, and a query scans only the few
clusters nearest to it. The index is persisted in the same database and
loaded on restart without retraining. I benchmarked the recall/latency
trade-off on synthetic data up to 500k vectors and on real embeddings, and
fine-tuned the model with contrastive learning on SciFact, improving nDCG@10
from 0.645 to 0.699 on held-out test queries.

### 2. Why build this on your own storage engine instead of pgvector, Qdrant or FAISS?

Because the point was to understand and own every layer, and because byodb
already gave me the hard part — durable, crash-safe transactions — so the
vector database could be honest about persistence instead of rebuilding an
in-memory index from a JSON file. Using FAISS would have made the ANN index a
black box; using pgvector would have made storage one. The cost is that my
index is slower than FAISS (no SIMD, no quantization), which the benchmarks
show rather than hide. For a production system with a team, I would use a
proven engine; this project is about being able to explain one.

### 3. How do you store a 384-dimensional vector in a B+tree with a 3,000-byte value limit?

byodb's key/value codec escapes the bytes 0x00 and 0x01 as two bytes, so an
arbitrary float32 payload can double in size. I store each vector as
segments of 256 floats (1,024 raw bytes, at most 2,048 after escaping), keyed
by `(vector_id, segment)`. A 384-d vector is two rows. The worst case is
tested with adversarial vectors whose bytes are all escape characters
(`vector/codec.go`, `store/store_test.go`). The tables are:

| table | key | holds |
|---|---|---|
| `vf_documents` | doc_id | source, path, content hash, chunker version, model ID, metadata (secondary index on source, path) |
| `vf_chunks` | (doc_id, ordinal) | text, section, line range, token count, vector_id (secondary index) |
| `vf_vectors` | (vector_id, segment) | float32 segment |
| `vf_ivf_centroids` | (list_id, segment) | centroid segments |
| `vf_ivf_postings` | (list_id, vector_id) | list membership (secondary index on vector_id) |
| `vf_meta` | key | schema version, model/dimension config, next vector ID, IVF metadata |
| `vf_ingest_runs` | run_id | audit trail of ingest runs |

### 4. What is an embedding, and why cosine similarity?

An embedding maps text to a point in \(\mathbb{R}^{384}\) such that texts
with similar meaning are close. MiniLM produces one by running a 6-layer
transformer and mean-pooling the token outputs. Cosine similarity compares
direction and ignores length, which is what the model was trained to make
meaningful. I normalize every vector to unit length once, so cosine is just
a dot product, and \(\lVert a-b\rVert^2 = 2-2\,a\cdot b\) means Euclidean
ranking is identical. Scores are only meaningful relative to one model; the
database refuses to mix vectors from different model IDs.

### 5. How fast is exact search, and what limits it?

It is \(O(N\cdot d)\): every query computes a dot product with every vector.
Single-threaded p50 latency at 384 dimensions was 1.26 ms at 10k vectors,
13.5 ms at 100k and 76.7 ms at 500k — linear, about 2.5–3 billion
multiply-adds per second per core with a 4-way unrolled scalar loop (Go does
not auto-vectorize). Under concurrency it stopped scaling at about 3× (194
QPS with 8 goroutines on 6 cores): each query streams 154 MB of vectors, so
that is ~30 GB/s. Two other workloads larger than the 32 MB L3 cache
plateaued at the same rate — exact search over Quora's 803 MB at 33 GB/s and
IVF over Quora (~38 MB scanned per query) at 31 GB/s — while exact search
over SciFact, whose 8 MB fits in cache, scaled 5× to 49 GB/s, more than DRAM
can deliver. One ceiling shared by three workloads, gone when the data fits
in cache, points to memory bandwidth at about 55 % of the theoretical
57.6 GB/s. That is an inference from throughput, not a separate memory
benchmark. Practical consequence: SIMD would speed up one query, but for
throughput on large collections, fewer bytes per vector (quantization) is
the lever.

### 6. How does IVF-Flat work?

Training: run k-means with \(K=\)`nlist` on (a sample of) the vectors to get
centroids, then assign every vector to its nearest centroid; each centroid
owns an inverted list of vector IDs. Query: score the query against all
centroids, pick the `nprobe` closest, and scan only those lists exactly.
Cost per query is roughly \(\text{nlist}\cdot d + \frac{\text{nprobe}}{\text{nlist}}N\cdot d\).
The approximation is that a true neighbour may sit in a list that was not
probed. "Flat" means vectors in the lists are stored uncompressed, so the
only error is which lists are scanned. My k-means is spherical (centroids
re-normalized, cosine assignment), seeded with k-means++, capped at 25
iterations, deterministic for a given seed (`index/kmeans.go`).

### 7. How do you choose nlist and nprobe?

`nlist` trades centroid-scoring cost against list-scanning cost, so the
default is \(\text{round}(\sqrt{N})\), which balances the two terms above.
The benchmarks show both failure directions: with 1,264 lists over 100k
vectors, a single-probe query still cost ~220 µs because scoring 1,264
centroids dominated scanning ~80 vectors, while with too few lists each probe
scans a large fraction of the data. `nprobe` is a query-time knob: on real
MiniLM embeddings of 523k Quora questions with nlist 723 (= √N), nprobe 8
gave recall@10 0.914 at 1.18 ms p50, nprobe 16 gave 0.949 at 2.21 ms and
nprobe 128 gave 0.993 at 16.3 ms, against 87.2 ms for exact search. Doubling
nlist to 1,446 bought slightly higher recall at every latency (0.964 at
2.37 ms) for 4× the build time (527 s vs 134 s). The default of 8 is a
recall-first setting for the demo corpus; the API accepts `nprobe` per
request.

### 8. Why IVF and not HNSW?

IVF's state is a list of centroids plus "vector → list" postings, which maps
cleanly onto database rows, rebuilds deterministically, and makes deletes a
row delete. HNSW typically gives better recall at a given latency, but it is
a graph whose edges change on every insert, deletes need tombstones and
repair, and persisting it transactionally means writing many neighbour lists
per insert. For a first index on a transactional store, IVF had the better
correctness-to-effort ratio. HNSW was a stretch goal and is not implemented.

### 9. What happens to the IVF index on insert, update and delete?

The database is the source of truth. An insert is assigned to its nearest
existing centroid and a posting row is written in the same transaction as the
vector; an update is a delete plus an insert with a fresh vector ID; a delete
removes vector, chunk and posting rows together. In-memory indexes are
updated only after the commit succeeds. Centroids are not retrained
incrementally, so they drift as data changes; the engine counts
`vectors_since_train` and an explicit rebuild retrains from the stored
vectors with a fixed seed and swaps the new state in one transaction. The
failure mode to expect is an insert unlike anything in the training data:
it lands in whichever list is least far away, which a query about it may
not probe. That is why `e2e.py` checks that an uploaded off-topic note is
found by exact search, but only records (without asserting) whether IVF
finds it; in the recorded run it did.

### 10. How is the index persisted, and how do you know it is correct after a restart?

Centroids, metadata (nlist, seed, model ID, generation) and postings are rows
in the same byodb file, written by `ReplaceIVF` in one transaction. At
startup the engine streams vectors into the exact index and rebuilds the IVF
lists from postings — it never retrains — and reports
`ivf_loaded: true, ivf_retrained: false`. Evidence:

- Storage benchmark: for 100k vectors the reloaded index returned results
  identical to the in-memory index on 200 queries; cold load took 1.75 s
  (1.54 s streaming vectors, 0.20 s rebuilding lists from postings) versus
  10.8 s to retrain k-means.
- `e2e.py` kills the server process (no graceful shutdown), restarts it, and
  checks that search results (IDs, ranks, scores) are identical, then repeats
  after inserts, an update and deletes: all 17 checks passed. The restart
  after the kill reported `ivf_retrained: false` and loaded the IVF in 9 ms,
  and after a second kill the added, modified and deleted files were all
  still in their new state.
- `GET /api/v1/index/verify` cross-checks documents, chunks, vectors,
  postings and memory; the demo database verifies clean at 7,478 vectors.

### 11. What consistency guarantees does it give, and what happens if it crashes mid-ingest?

Ingestion commits documents in batches, and each batch is one byodb
transaction: for every document in it, the old chunks, vectors and postings
are removed and the new ones written. A crash therefore leaves each document
at either its old or its new version, never a mix. byodb provides the
atomicity (copy-on-write pages, two checksummed meta pages). There is one
writer at a time; a second mutation gets HTTP 409 instead of queueing.
Searches take a read lock on the in-memory indexes and read chunk text from a
snapshot, so they never see a half-applied change. On startup the engine
marks ingest runs that were running as `interrupted`, repairs vectors with a
missing posting, ignores postings that point at nothing, and reports each
count.

### 12. How does ingestion avoid duplicates and partial-failure damage?

Document IDs are derived from `sha256(source, path)`, so re-ingesting a file
targets the same document. A file is skipped when its content hash, chunker
version and model ID all match what is stored; otherwise it is re-chunked,
re-embedded and replaced atomically. Files missing from the source are
deleted. Embedding happens before the transaction opens. If the embedding
service fails, the run stops with status `failed`: batches already committed
stay committed, nothing from the failing batch is written, and the deletion
pass for files missing from the source is skipped, so a partial run never
deletes anything. Rerunning skips what was committed (hashes match) and
continues. An unreadable file keeps its previously indexed version and is
listed in the run's errors. `e2e.py` checks that a second ingest of an
unchanged corpus embeds zero chunks.

### 13. How do you chunk, and why does it matter?

Chunks are the unit of retrieval, so they need to be about one idea and fit
the model. Markdown splits on headings, Go on top-level declarations with
their doc comments (via `go/ast`, falling back to line windows if a file does
not parse), Python on `def`/`class` (large classes at methods, never inside
triple-quoted strings), JavaScript/TypeScript on top-level declarations, and
text on paragraphs. Small neighbours are packed together; units over the
budget are split on line boundaries with two lines of overlap. Each chunk is embedded as
`path · section` plus its text, so the file and symbol name contribute to the
vector. Budgets are per kind: measured with the model's own tokenizer on the
demo corpus, Go averages about 2.5 bytes per token against 3.5 for Markdown,
so code gets a smaller byte budget. That change cut the share of chunks
truncated at 256 tokens from 65 % to 16 %.

### 14. What is contrastive learning, and what are hard negatives?

Contrastive learning trains the encoder by comparison: for a query, push its
relevant document's similarity up and every other candidate's down. I used
the InfoNCE loss — a softmax cross-entropy over similarity scores scaled by
1/temperature (20) — where the other queries' positives in a batch of 32 act
as free negatives. Random negatives are mostly easy; hard negatives are
documents that look relevant but are not. I mined them with BM25 from ranks
3–29 of each training query, excluding labelled positives, and skipped the
top ranks because unlabelled top BM25 hits are often actually relevant
(false negatives). Documents that are positives for two queries in a batch
are masked so the model is never trained to push a query away from a
relevant document.

### 15. How do you measure retrieval quality? Define MRR, Recall@K and nDCG.

- Recall@K: fraction of a query's relevant documents in the top K.
- MRR@10: mean over queries of 1/rank of the first relevant result (0 if
  none in the top 10). It rewards getting one right answer to the top.
- nDCG@10: \(\sum_i g_i/\log_2(i+1)\) over the top 10, divided by the same sum
  for the ideal ordering; it credits graded relevance and position.

All are macro-averaged over queries. With 300 test queries, differences of a
point or two can be noise, so I report paired bootstrap confidence intervals
on per-query differences.

### 16. Did fine-tuning help, and how do you know it is not noise?

On SciFact's 300 held-out test queries, nDCG@10 went from 0.645
(pretrained) to 0.699 (fine-tuned with hard negatives): +0.054, 95 % CI
[+0.037, +0.073]; 61 queries improved, 23 got worse. Test labels were never
used for training, negative mining or checkpoint selection — I split BEIR's
training queries 85/15 into train and validation and selected epochs on
validation. Hard negatives added +0.016 over in-batch-only training on test
(CI [+0.005, +0.028]), but validation ranked the two the other way, by less
than 121 validation queries can resolve, and I trained one seed per config,
so I describe that part as a small single-seed effect. Also worth saying:
pretrained MiniLM did not beat BM25 on SciFact (−0.016, CI spans zero).

### 17. What is the difference between ANN recall and relevance?

ANN recall compares the index against exact search with the same model; it
measures the database. Relevance metrics compare results against human
labels; they measure the model and index together. They can disagree. On
Quora, IVF at nprobe 8 had ANN recall@10 0.914 yet lost only 1.6 % of
nDCG@10 (0.860 vs 0.874), so most of the neighbours it missed were not
labelled relevant; on SciFact, ANN recall 0.947 still cost 4.6 % (0.616 vs
0.645). On my 44-query code-search set, IVF at nprobe 8 scored slightly
*above* exact search (nDCG@10 0.825 vs 0.810, within noise: 2 queries
improved, none worsened) — when the index skips an exact neighbour, the
replacement can be the relevant one. The model sets the ceiling; the index
decides how much of it you keep at a given latency.

### 18. Where does a search request spend its time?

Measured through the HTTP API on the 7,478-vector demo corpus: p50 13.4 ms
per exact search, of which 10.2 ms was embedding the query on the CPU,
1.3 ms exact scoring and 1.3 ms fetching chunk texts from byodb, the rest
HTTP and JSON. With IVF (nprobe 8) index search fell to 0.14 ms and the
request to 12.1 ms. Under 8 concurrent clients throughput stayed at 39–68
QPS across three runs, because the embedding service runs one CPU model and
serializes calls. Query embedding dominates at this corpus size; index
tuning matters at hundreds of thousands of vectors and up, where exact
search costs tens of milliseconds. The fix for the embedding bottleneck is
batching concurrent queries into one forward pass, or a GPU.

### 19. How does it handle concurrency?

Many readers, one writer. Searches hold a read lock on the in-memory indexes
only while scoring, then fetch chunk text from a byodb read snapshot. Writes
are serialized by a mutex acquired with `TryLock` (busy → HTTP 409), do all
slow work (reading files, embedding) before opening the transaction, and
update memory after commit under the write lock. The test suite runs with
`-race`, including a test that uploads, deletes and rebuilds while searches
run concurrently.

### 20. How would you scale this to 100 million vectors?

Reasoning, not measured here:

- **Memory.** 100M × 384 × 4 bytes is 154 GB of raw floats. Product
  quantization (IVF-PQ) or int8 scalar quantization cuts that 4–32×, with a
  re-scoring pass on full vectors for the top candidates.
- **Compute.** SIMD dot products (assembly or cgo) for a several-fold speed-up
  per core; but multi-threaded search over large collections already hit a
  ~30 GB/s memory-bandwidth ceiling here, so compression helps throughput
  more than faster arithmetic.
- **Sharding.** Partition by document ID hash, search shards in parallel and
  merge top-k (scatter-gather); replicate shards for read throughput.
- **Index.** HNSW or IVF with many more lists, plus a second-level quantizer
  for centroid search once nlist is in the tens of thousands.
- **Ingestion.** A queue in front of a batched, GPU-backed embedding fleet;
  index updates applied asynchronously with a bounded staleness.
- **Quality.** Hybrid lexical + dense retrieval and a cross-encoder reranker
  on the top 100.

### 21. What was the most surprising thing you found?

Three things. First, Go's monotonic clock on Windows advances in roughly
0.5 ms steps, so sub-millisecond benchmark latencies were quantized garbage
until I added a QueryPerformanceCounter-based timer (`hrtime/`). Second, my
first chunk budgets were in bytes, and 65 % of chunks were silently truncated
by the model's 256-token limit; I only found it by measuring with the real
tokenizer. Third, synthetic clustered data made IVF look perfect (recall 1.0
at one probe), while isotropic data made it look useless (0.94 recall only
after scanning 81 % of vectors); real embeddings sit in between, which is
why the headline numbers use real embeddings.

### 22. What is missing, and what would you do next?

HNSW; SIMD and quantization; hybrid BM25 + dense ranking and a reranker;
multiple training seeds; a FAISS comparison as an external reference; a
Docker setup (Docker was not available on the development machine). The
fine-tuned SciFact model is evaluated but not served, because it is tuned
for scientific claims and the demo corpus is code.
