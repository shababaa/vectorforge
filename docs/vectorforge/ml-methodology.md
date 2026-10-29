# VectorForge ML methodology

This document covers the learned half of VectorForge: what the embedding model
computes, how similarity is scored, how the model was fine-tuned, how retrieval
quality was measured, and how model quality relates to (and differs from)
database performance. Every number here is copied from a JSON file under
`vectorforge/results/` produced by the code in `vectorforge/ml/vfml/`; the file
is named next to each table.

## 1. Embeddings

An embedding model is a function \(f:\text{text}\to\mathbb{R}^{d}\) trained so
that texts with similar meaning land close together. VectorForge uses
[`sentence-transformers/all-MiniLM-L6-v2`](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2):

| property | value |
|---|---|
| architecture | 6-layer MiniLM transformer, hidden size 384, ~22.7 M parameters |
| pooling | mean of the final token embeddings (padding masked out) |
| output | 384-dimensional, then L2-normalized |
| input limit | 256 WordPiece tokens; longer input is truncated |
| pretraining | contrastive training on ~1 B sentence pairs (per the model card) |

The service (`vfml/service.py`, FastAPI) wraps it:

- `GET /health` reports the model ID, dimension, `max_seq_length`, batch limit.
- `POST /embed` and `POST /embed/batch` (≤ 256 texts, ≤ 8,192 characters each)
  return unit-length float32 vectors. Invalid input is HTTP 422.
- Text is preprocessed identically for queries and documents: Unicode NFC,
  CRLF → LF, control characters stripped, surrounding whitespace trimmed
  (`vfml/preprocess.py`). Code indentation is preserved.
- The model ID is pinned to the cached Hub snapshot revision
  (`sentence-transformers/all-MiniLM-L6-v2@1110a243fdf4`), or to a SHA-256 of
  the weights for a local directory. The database records the model ID and
  dimension in its config row and on every document; the engine refuses to
  open a database with a different model (`engine_test.go`), and the IVF
  metadata carries the model ID it was trained for. Vectors from two models are
  never compared with each other.

**Why one vector per chunk, not per file.** Mean pooling over 256 tokens
already blurs a passage into one point; pooling a whole file would blur it
further and hit the truncation limit. The ingestion pipeline therefore splits
files into chunks along structure (Markdown headings, Go/Python declarations)
and embeds `path · section` followed by the chunk text, so the vector knows
where the text came from. See [architecture.md](architecture.md#write-path-ingestion).

## 2. Cosine similarity

For vectors \(a, b\):

\[
\cos(a,b) = \frac{a\cdot b}{\lVert a\rVert\,\lVert b\rVert}\in[-1,1]
\]

Every stored and query vector is normalized to unit length once, at write or
query time, so the score is just the dot product \(a\cdot b\), a single fused
multiply-add loop over 384 floats (`vector/similarity.go`, 4-way unrolled).
For unit vectors

\[
\lVert a-b\rVert^2 = 2 - 2\,a\cdot b
\]

so ranking by cosine, by dot product and by ascending Euclidean distance give
the same order. The `vector` package implements all three plus normalization
(which rejects zero, NaN and infinite vectors) and a heap-based top-k with a
deterministic tie-break on ID.

Cosine scores are only comparable within one model. A score of 0.36 is not
"36 % relevant"; it is a position in this model's geometry. The UI shows the
score for ordering, not as a probability.

## 3. Contrastive fine-tuning

`vfml/train.py` fine-tunes the bi-encoder with the InfoNCE / multiple-negatives
ranking loss. For a batch of \(B\) training triples (query \(q_i\), labelled
positive \(p_i\), hard negative \(n_i\)) the candidate set is
\(C = [p_1,\dots,p_B, n_1,\dots,n_B]\) and

\[
s_{ij} = \tau^{-1}\cos(q_i, c_j), \qquad
\mathcal{L} = \frac{1}{B}\sum_{i=1}^{B} -\log\frac{\exp(s_{ii})}{\sum_{j}\exp(s_{ij})}
\]

This is cross-entropy where the "class" of query \(i\) is its own positive.
Pulling \(s_{ii}\) up and every other \(s_{ij}\) down moves the query toward
its relevant document and away from everything else in the batch. With
\(B=32\), each query sees 31 in-batch negatives (other queries' positives) and
32 hard negatives. \(\tau^{-1} = 20\) (temperature 0.05) sharpens the softmax so
the loss focuses on the negatives that are closest to the query.

Two details matter:

- **Masking shared positives.** SciFact has documents relevant to more than one
  query. If such a document appears as another query's positive in the same
  batch, treating it as a negative would push the query away from a document
  it is relevant to. Those cells are set to \(-\infty\) before the softmax.
- **Explicit loop.** The loop (tokenize, forward, similarity matrix, masked
  cross-entropy, gradient clipping at 1.0, AdamW with 10 % linear warmup and
  linear decay) is written out rather than delegated to a trainer class, so the
  objective is inspectable.

### Hard negatives

Random in-batch negatives are mostly easy: an abstract about protein folding is
obviously not about vaccine trials. Hard negatives are documents that look
relevant to a cheap retriever but are not labelled relevant, which forces the
model to learn the distinction the cheap retriever misses.

`mine_hard_negatives` runs BM25 for each **training** query, removes the
query's labelled positives, and keeps ranks 3–29. The top few are skipped on
purpose: SciFact labels are sparse (about 1.1 judged positives per query), so
the very top BM25 hits that are unlabelled are the most likely to be *false
negatives*, relevant documents nobody labelled, and training against them
would teach the model the wrong thing. One negative per triple is sampled from
that window each epoch.

The ablation `inbatch` trains with the same seed and schedule but no hard
negatives, isolating their effect.

## 4. Metrics

All metrics are computed per query and macro-averaged over judged queries
(`vfml/metrics.py`, BEIR/pytrec_eval conventions). A judged query that is
missing from a run counts as a complete miss. Let \(R_q\) be the relevant set
and \(r_1, r_2, \dots\) the ranked results.

- **Recall@K** — the fraction of relevant documents retrieved in the top K:
  \(\lvert R_q \cap \{r_1..r_K\}\rvert / \lvert R_q\rvert\).
- **MRR@10** — the reciprocal rank of the first relevant result, 0 if none is
  in the top 10: \(\frac{1}{|Q|}\sum_q 1/\text{rank}_q\). It rewards putting
  *one* right answer first, which is what a search box user feels.
- **nDCG@10** — discounted cumulative gain normalized by the ideal ordering:

  \[
  \text{DCG@10}=\sum_{i=1}^{10}\frac{g(r_i)}{\log_2(i+1)},\qquad
  \text{nDCG@10}=\frac{\text{DCG@10}}{\text{IDCG@10}}
  \]

  with linear gain \(g\) = the graded label. It credits every relevant result
  but discounts lower positions, and it is BEIR's headline metric.
- **P@1** — whether the top result is relevant.

Worked example from `tests/test_text_and_metrics.py`: relevant
\(\{d_3{:}1, d_7{:}2\}\), ranking \([d_1, d_3, d_2, d_7]\). The first relevant
result is at rank 2, so RR = 0.5. Recall@2 = 1/2 (only \(d_3\)), Recall@4 = 1.
For nDCG with relevant \(\{a{:}2, b{:}1\}\) and ranking \([b, a, c]\):
DCG = \(1/\log_2 2 + 2/\log_2 3 = 2.262\), IDCG = \(2 + 1/\log_2 3 = 2.631\),
nDCG = 0.860.

**Significance.** With 300 test queries, small differences are noise.
Comparisons use a paired bootstrap: per-query nDCG@10 differences are
resampled with replacement 10,000 times (seed 1), giving a 95 % confidence
interval for the mean difference and the fraction of resamples where the
difference is ≤ 0. The report also counts how many queries improved, worsened
or stayed the same.

### ANN recall is not relevance

Two different "recalls" appear in this project and they must not be confused:

| | compares against | measures | depends on |
|---|---|---|---|
| **ANN recall@k** | the exact top-k of the *same model* | how faithfully the index finds the model's nearest neighbours | the index (nlist, nprobe), not labels |
| **Recall@K (retrieval)** | human relevance labels | whether the right documents were found | the model *and* the index |

ANN recall of 1.0 with a weak model is precisely wrong; ANN recall of 0.9 with
a strong model can still be very useful. Section 8 measures both on the same
runs.

## 5. Protocol

**Data.** BEIR SciFact: 5,183 scientific abstracts, claims as queries,
graded relevance labels. The archive is downloaded once and pinned by SHA-256
in `vectorforge/ml/datasets.lock.json`; its MD5 matches the value published in
the BEIR README.

| split | queries | used for |
|---|---|---|
| train (BEIR train, 85 %) | 688 | fine-tuning and hard-negative mining |
| validation (BEIR train, 15 %) | 121 | epoch selection |
| test (BEIR test) | 300 | the reported numbers, evaluated once per model |

The 85/15 split of BEIR's 809 training queries is a seeded shuffle (seed 2027);
SHA-256 digests of each split's query IDs are stored in the report. The code
asserts the splits are disjoint. **Test labels are never used for training,
negative mining or model selection.** The corpus is shared across splits, as in
BEIR, so a document can be a training positive for one claim and a test
positive for another; that is the standard setting and uses no test labels.

**Selection.** Each configuration trains for 3 epochs. After every epoch the
model is evaluated on validation and the checkpoint with the best validation
nDCG@10 is kept. Epoch 0 is the pretrained model itself, so if no epoch beats
it, the pretrained weights are selected and the report says so; fine-tuning
cannot "win" by default.

**Configuration** (`TrainConfig`): batch 32, learning rate 2e-5, weight
decay 0.01, warmup 10 %, temperature 0.05, max sequence length 256, gradient
clipping 1.0, training seed 13, hard negatives from BM25 ranks [3, 30).

**Baselines.** BM25 (`vfml/bm25.py`, k1 = 0.9, b = 0.4 as in Anserini's BEIR
runs; lowercase alphanumeric tokens, English stopwords removed, no stemming)
over the same `title + " " + text` strings, and the pretrained model.

**Environment.** CPU only: AMD Ryzen 5 5600 (6 cores / 12 threads), Windows,
Python 3.13.3, torch 2.14.1+cpu, sentence-transformers 6.1.0. Reproduce with
`python -m vfml.scifact` from `vectorforge/ml` (`--smoke` runs a one-minute
version on a subset; its output is flagged `"smoke": true` and is not a
result).

## 6. SciFact results

Source: `vectorforge/results/ml/scifact.json` (full run, 43.5 min wall time,
`"smoke": false`). Test split, 300 queries, macro-averaged.

| system | nDCG@10 | MRR@10 | P@1 | Recall@10 | Recall@100 | nDCG@100 |
|---|---|---|---|---|---|---|
| BM25 | 0.6608 | 0.6300 | 0.5433 | 0.7784 | 0.8859 | 0.6864 |
| MiniLM-L6-v2, pretrained | 0.6451 | 0.6047 | 0.5033 | 0.7833 | 0.9250 | 0.6766 |
| fine-tuned, in-batch negatives only | 0.6830 | 0.6370 | 0.5333 | 0.8464 | 0.9533 | 0.7070 |
| **fine-tuned, in-batch + BM25 hard negatives** | **0.6994** | **0.6520** | **0.5500** | **0.8600** | **0.9533** | **0.7196** |

Paired bootstrap on per-query nDCG@10 (10,000 resamples, seed 1):

| comparison | mean Δ nDCG@10 | 95 % CI | P(Δ ≤ 0) | improved / worsened / same |
|---|---|---|---|---|
| pretrained − BM25 | −0.0157 | [−0.0549, +0.0237] | 0.78 | 67 / 73 / 160 |
| in-batch − pretrained | +0.0380 | [+0.0221, +0.0550] | 0.000 | 55 / 27 / 218 |
| hard-neg − pretrained | **+0.0543** | **[+0.0373, +0.0729]** | 0.000 | 61 / 23 / 216 |
| hard-neg − in-batch | +0.0163 | [+0.0053, +0.0279] | 0.0015 | 40 / 26 / 234 |

The fine-tuned model against BM25 on the same 300 queries (computed by
`vfml.annrelevance` from the Go exact-search run, section 8): Δ nDCG@10
+0.0385, 95 % CI [+0.0009, +0.0771], 85 improved / 53 worsened / 162
unchanged. Fine-tuning turns a statistical tie with BM25 into a small win
whose interval only just excludes zero.

Validation nDCG@10 (121 queries) after each epoch; the selected checkpoint is
marked. Epoch 0 is the pretrained model.

| config | epoch 0 | epoch 1 | epoch 2 | epoch 3 | mean train loss by epoch | seconds per epoch |
|---|---|---|---|---|---|---|
| hard-neg | 0.6444 | 0.6878 | 0.6932 | **0.6959** ✓ | 0.535, 0.323, 0.258 | 281, 275, 279 |
| in-batch | 0.6444 | 0.6776 | 0.6893 | **0.6975** ✓ | 0.265, 0.157, 0.111 | 181, 176, 167 |

(The losses of the two configs are not comparable with each other: the
hard-negative softmax is over 64 candidates per query instead of 32, so its
loss is higher for the same model.)

**Cost.** 789 training pairs per epoch, 25 optimizer steps of 32. Wall time
including validation: 20.3 min (hard-neg) and 16.1 min (in-batch) on 6 CPU
threads. Encoding the 5,183-document corpus with the pretrained model took
121 s. The fine-tuned checkpoint is 91.6 MB on disk (pretrained snapshot:
91.6 MB) — fine-tuning changes weights, not architecture, so vector
dimension, database size and search cost are unchanged.

**Query encoding latency** (batch size 1, CPU, 6 torch threads). The
per-model numbers in `scifact.json` were taken minutes apart, right after
each model finished training, and differ (p50 8.3 / 10.6 / 9.3 ms) even though
the three models have identical architectures. `vfml.encode_latency`
re-measured them interleaved, round by round, on the same 100 test claims
(mean 91 characters), so drift affects all three equally
(`results/ml/encode-latency.json`):

| model | p50 | p95 | p99 | mean |
|---|---|---|---|---|
| pretrained | 11.42 ms | 14.63 ms | 16.73 ms | 12.16 ms |
| fine-tuned, hard negatives | 11.90 ms | 16.61 ms | 22.09 ms | 12.41 ms |
| fine-tuned, in-batch | 11.62 ms | 17.18 ms | 26.02 ms | 12.77 ms |

Fine-tuning does not change inference cost. The absolute level moved by
about 40 % between the two sessions (8.3 ms vs 11.4 ms p50 for the same
pretrained model), which is the run-to-run variation to expect from a
desktop machine; compare models within one session, not across sessions.

### What the numbers say

1. **The pretrained model does not beat BM25 on SciFact** (−0.016 nDCG@10, CI
   spanning zero). Scientific claims share exact technical terms with their
   evidence, which is where lexical matching is strong; BEIR reports the same
   pattern for many dense models out of domain. Both baselines line up with
   published values (BEIR paper: BM25 0.665; the MTEB leaderboard lists 64.51
   for all-MiniLM-L6-v2 on SciFact), which is a check that the evaluation
   pipeline is computing the standard metric.
2. **Contrastive fine-tuning on 688 claims improves test nDCG@10 by +0.054**
   with a confidence interval well clear of zero. 61 queries improved, 23
   got worse, 216 were unchanged. Recall@100 rises from 0.925 to 0.953, which
   matters for an ANN index: more of the relevant documents sit in the
   candidate set that a reranker or a user would see.
3. **Hard negatives help, by less than fine-tuning itself.** On test they add
   +0.016 over in-batch-only training (CI [0.005, 0.028]). On the 121-query
   validation split the order was reversed (0.6959 vs 0.6975), a difference
   far below what 121 queries can resolve. Each config was trained with one
   seed, so seed-to-seed variance is not measured; the honest summary is
   "hard negatives were better on the held-out test set, by a small but
   statistically separable margin, in a single-seed experiment".
4. **Fine-tuning hurt some queries.** 23 of 300 test queries scored lower
   after hard-negative fine-tuning. A specialised model is not uniformly
   better; that is why the comparison is reported per query and not only as a
   mean.

The fine-tuned model is **not** used for the demo corpus. It was tuned on
scientific claims, while the demo corpus is source code and engineering
documentation; switching models also invalidates every stored vector and the
IVF centroids. The engine enforces that: a database records its model ID, and
re-embedding into a new database is the only supported way to change models.

## 7. Code search on the demo corpus

Source: `vectorforge/results/ml/codesearch.json`, produced by
`python -m vfml.codesearch` against the running server (commit `538c6b0`,
7,478 chunks from 357 files of this repository and 15 Go standard-library
packages, IVF nlist = 86).

**Query set** (`vectorforge/eval/codesearch.json`): 44 queries with graded
file-level labels (2 = the primary file, 1 = related) — 29 about the Go
standard library (26 natural-language, 3 exact identifiers such as
`MaxIdleConnsPerHost`) and 15 about this repository. The queries and labels
were written by the same AI assistant that implemented VectorForge, from the
file inventory and source, before any of them were run; they were not tuned
on system output. They are a small single-annotator set: indicative, not a
benchmark. The `vectorforge/eval` and `docs/vectorforge` directories are
excluded from ingestion so the queries cannot be retrieved as documents.

**Protocol.** Each system retrieves 100 chunks; chunks are collapsed to files
by first occurrence, and metrics are computed over the file ranking. BM25 is
built over exactly the strings the model embeds (path · section header +
text), with identifier splitting that keeps the whole identifier and adds
its parts (`seekLE` → `seekle`, `seek`, `le`). Dense runs go
through the HTTP API, so they include the real chunking, embedding and index.
A SHA-256 digest of the corpus is stored with the results.

| system | nDCG@10 | MRR@10 | Recall@10 | ANN recall@10 (chunks) | index search p50 (top-100) |
|---|---|---|---|---|---|
| BM25 | 0.704 | 0.707 | 0.818 | — | — |
| dense, exact | 0.810 | 0.768 | 0.960 | 1.000 | 1.36 ms |
| dense, IVF nprobe 1 | 0.653 | 0.667 | 0.710 | 0.532 | 0.05 ms |
| dense, IVF nprobe 2 | 0.768 | 0.763 | 0.858 | 0.732 | 0.07 ms |
| dense, IVF nprobe 4 | 0.805 | 0.791 | 0.926 | 0.827 | 0.11 ms |
| dense, IVF nprobe 8 (server default) | 0.825 | 0.791 | 0.960 | 0.941 | 0.17 ms |
| dense, IVF nprobe 16 | 0.825 | 0.791 | 0.960 | 0.966 | 0.30 ms |
| dense, IVF nprobe 32 | 0.810 | 0.768 | 0.960 | 0.984 | 0.59 ms |

Paired bootstrap on nDCG@10 (44 queries):

| comparison | Δ | 95 % CI | improved / worsened / same |
|---|---|---|---|
| dense exact − BM25 | +0.106 | [+0.011, +0.211] | 16 / 9 / 19 |
| IVF nprobe 4 − exact | −0.005 | [−0.057, +0.046] | 7 / 4 / 33 |
| IVF nprobe 8 − exact | +0.015 | [0.000, +0.039] | 2 / 0 / 42 |
| IVF nprobe 1 − exact | −0.157 | [−0.282, −0.036] | 10 / 14 / 20 |

By query group (nDCG@10): Go standard library, BM25 0.858 vs dense 0.875;
this repository, BM25 0.406 vs dense 0.685; the three identifier queries
score 1.0 for both.

What this shows, with the sample-size caveat:

- Dense retrieval beats BM25 overall, and the gap comes almost entirely from
  this repository's queries, which are phrased in words that differ from the
  code ("how are torn commits detected"). On the standard library, where
  queries tend to share vocabulary with doc comments, BM25 is close.
- **IVF scored above exact search at nprobe 8 and 16.** That is not IVF being
  better. IVF scores the chunks it does examine exactly, so it can only
  differ from exact search by missing chunks; for a relevant file to move up,
  chunks of an irrelevant file ranked above it must have been missed. That
  happened on two queries (none got worse). It is a direct demonstration
  that ANN recall and relevance are different quantities. With 44 queries the
  difference is within noise.
- At the server's default nprobe 8, IVF probes 8 of 86 lists and searches in
  0.17 ms instead of 1.36 ms, with no measurable relevance loss on this set.

## 8. What approximate search costs in relevance

Source: `vectorforge/results/ml/ann-relevance.json`, produced by
`vfml.annrelevance` from the top-100 run files that `vfbench -suite real`
wrote for exact search and for IVF at nlist = round(√N) and every nprobe.
The Go benchmark never sees labels; the Python scorer joins its rankings with
the BEIR qrels afterwards. As a consistency check, the Go exact index
reproduces the Python evaluation's SciFact numbers to four decimals (0.6451
pretrained, 0.6994 fine-tuned).

Two datasets with different shapes:

- **SciFact**, 5,183 abstracts, 300 test claims, nlist = 72. Asymmetric: short
  queries, long documents.
- **Quora**, 522,931 questions, 1,000 test queries sampled with seed 2027
  from the 10,000 in BEIR's test split, nlist = 723, pretrained MiniLM.
  Symmetric: queries and documents are the same kind of text. Following BEIR,
  a document with the query's own ID is removed before scoring. Encoding the
  corpus took 29 min on CPU (304 documents/s).

| dataset / model | nprobe | lists probed | ANN recall@10 | nDCG@10 | Δ vs exact [95 % CI] |
|---|---|---|---|---|---|
| Quora / pretrained | exact | — | 1.000 | 0.8739 | — |
| | 4 | 0.6 % | 0.863 | 0.8428 | −0.0311 [−0.0415, −0.0216] |
| | 8 | 1.1 % | 0.914 | 0.8600 | −0.0139 [−0.0203, −0.0082] |
| | 16 | 2.2 % | 0.949 | 0.8664 | −0.0075 [−0.0124, −0.0033] |
| | 32 | 4.4 % | 0.971 | 0.8699 | −0.0040 [−0.0078, −0.0010] |
| | 64 | 8.9 % | 0.985 | 0.8724 | −0.0015 [−0.0041, +0.0001] |
| SciFact / pretrained | exact | — | 1.000 | 0.6451 | — |
| | 8 | 11 % | 0.870 | 0.5955 | −0.0496 [−0.0737, −0.0268] |
| | 16 | 22 % | 0.947 | 0.6155 | −0.0296 [−0.0485, −0.0131] |
| | 32 | 44 % | 0.989 | 0.6430 | −0.0020 [−0.0063, +0.0002] |
| SciFact / fine-tuned | exact | — | 1.000 | 0.6994 | — |
| | 8 | 11 % | 0.874 | 0.6345 | −0.0649 [−0.0908, −0.0411] |
| | 16 | 22 % | 0.942 | 0.6692 | −0.0302 [−0.0480, −0.0148] |
| | 32 | 44 % | 0.985 | 0.6908 | −0.0086 [−0.0188, −0.0005] |

("Lists probed" is nprobe / nlist; the fraction of vectors scored is similar
but not identical because lists differ in size. Full tables, including
recall@100 and every nprobe: [benchmark-tables.md](benchmark-tables.md).)

Reading the table:

- On Quora the relevance loss is much smaller than the ANN miss rate (8.6 %
  of neighbours missed at nprobe 8 cost 1.6 % of nDCG@10). On SciFact it is
  closer to proportional (5.3 % missed at nprobe 16 cost 4.6 %). The likely
  reason is the geometry of the task: Quora's relevant duplicate is usually
  the single nearest neighbour (exact MRR@10 0.868), so it tends to share the
  query's cluster; SciFact's relevant abstract is more often further down the
  exact ranking (MRR@10 0.605), where neighbours are spread over more
  clusters. That explanation is an inference, not a separate measurement.
- IVF was statistically indistinguishable from exact search (interval
  containing zero) from ANN recall@10 ≈ 0.985 upward on both datasets. Below
  that, the loss is measurable even when it is small.
- Fine-tuning did not change how well the index approximates the model
  (ANN recall 0.874 vs 0.870 at nprobe 8), so its relevance gain carries over
  to IVF at every setting.
- The real data needs a far larger fraction of the corpus scanned than the
  synthetic clustered benchmark suggested, which is why recall claims in this
  project are made on real embeddings.

**BM25 on the same queries.** Quora: BM25 0.7097 vs dense exact 0.8739,
Δ +0.164 [+0.144, +0.185]. This BM25 (stopwords removed, no stemming) is
weaker on Quora than the BM25 reported in the BEIR paper (0.789), so the
margin overstates dense retrieval's advantage over a tuned lexical baseline.
SciFact: see the table in section 6.

## 9. Model quality versus database performance

A semantic search result passes through two independent approximations, and
they fail differently:

```
query text ──model──▶ query vector ──index──▶ top-k vectors ──▶ results
            (what "near" means)        (how faithfully "near" is found)
```

| | model quality | database / index performance |
|---|---|---|
| question | are the model's nearest neighbours the relevant documents? | does the index return the model's nearest neighbours, quickly? |
| measured by | nDCG@10, MRR@10, Recall@K against **human labels** | ANN recall@k against **exact search**; latency, QPS, build time, memory |
| improved by | better/fine-tuned model, better chunking, hybrid lexical scoring | more lists probed, better clustering, faster scoring code |
| failure looks like | confident, fast, exactly-computed wrong answers | right model, but a relevant neighbour sits in an unprobed list |
| cost of fixing | re-embed every document, retrain the index | a query-time parameter (nprobe) or a rebuild |

Practical consequences, each visible in this project's measurements:

- **The model sets the ceiling.** Exact search is the best the index can do.
  On SciFact, swapping the pretrained model for the fine-tuned one moved
  nDCG@10 by +0.054; no index setting can recover that.
- **How much relevance ANN error costs depends on the data.** On Quora
  (question → duplicate question), IVF at nprobe 8 missed 8.6 % of the exact
  top-10 but lost only 1.6 % of nDCG@10; the relevant duplicate is usually
  the query's nearest neighbour. On SciFact (short claim → long abstract) the
  loss was close to proportional: 5.3 % of neighbours missed at nprobe 16
  cost 4.6 % of nDCG@10. On both,
  IVF became statistically indistinguishable from exact search only at ANN
  recall@10 of about 0.985 (section 8). ANN recall is a proxy; the price of
  approximation has to be measured against labels on the target workload.
- **ANN error is not uniform.** The probability of a miss depends on where a
  vector sits relative to the centroids. An off-distribution document (one
  unlike anything the centroids were trained on) can land in a list that
  queries rarely probe; [architecture.md](architecture.md#known-limitations)
  records such a case, rank 1 under exact search and missing at nprobe 8.
  Averages hide this; per-query comparison exposes it.
- **Changing the model is a database migration.** New weights mean new
  vectors for every chunk and new centroids. VectorForge treats the model ID
  as part of the schema and refuses to mix them.
- **Latency budgets are dominated by the model.** Embedding a query costs
  milliseconds on CPU; scoring 7,000 vectors costs well under a millisecond
  (see the API breakdown in [benchmarks.md](benchmarks.md)). Index tuning
  matters at the scale of hundreds of thousands of vectors and above, which is
  why the index benchmarks go to 500k.

## 10. Limitations

- **Small test sets.** 300 SciFact test queries and 44 code-search queries.
  The bootstrap intervals are the honest error bars; differences inside them
  are not findings.
- **One training seed per configuration.** Seed-to-seed variance of
  fine-tuning was not measured.
- **The code-search queries were written by the same assistant that built the
  system** (disclosed in the file); they were not tuned against system
  output, but they are not an independent benchmark.
- **CPU only, one machine.** Absolute latencies are specific to a Ryzen 5 5600
  under Windows; ratios (speed-up, recall) transfer better than milliseconds.
- **Truncation.** Inputs over 256 tokens are truncated by the model. Chunk
  budgets keep this to a minority of chunks (16 % of the demo corpus by the
  service's tokenizer), but those chunks are represented by their first 256
  tokens only.
- **No reranker, no hybrid scoring.** BM25 is a baseline here, not part of the
  served ranking. A cross-encoder reranker or reciprocal-rank fusion with BM25
  is the obvious next quality step.
