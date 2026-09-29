# byodb: database engine + market intelligence platform

This repository now contains two connected projects:

1. **A database engine built from scratch in Go**: copy-on-write B+ trees,
   durable pages, recovery, transactions, relational tables, secondary indexes,
   and a SQL-like query language.
2. **A market intelligence system built on that engine**: validated OHLCV
   ingestion, indexed time-series queries, point-in-time features, model
   lineage, walk-forward forecasts, evaluation metrics, and auditable LLM
   analysis.

The combination is the portfolio story: the application does not hide behind
PostgreSQL or an ORM. Its domain data, features, predictions, and LLM outputs
are stored on a database whose storage and transaction layers are implemented
in this repository.

> This is an educational engineering and research project, not financial
> advice or a production trading system.

## Phase 5 full-stack demo

Start the complete offline service from an empty database:

```sh
go run ./cmd/marketserver -db service-demo.db -seed-demo
```

Open <http://127.0.0.1:8080>. One Go process now serves a responsive market
dashboard and bounded REST API, runs restart-aware background jobs, exports
Prometheus metrics and JSON logs, and owns the embedded database. The first
start produces the synthetic dataset, model card, saved prediction, and
grounded analyst record; later starts reuse those persisted artifacts.

Docker users can run the same acceptance path with `docker compose up --build`.
See the [service runbook](docs/SERVICE.md), [OpenAPI contract](docs/openapi.yaml),
and [Phase 5 validation record](docs/PHASE5_VALIDATION.md).

## Phase 6 database-systems demo

Create a durable snapshot, then serve that independent file without workers or
mutations:

```sh
# Stop any process currently owning service-demo.db before using the CLI.
go run ./cmd/byodb -db service-demo.db -backup service-replica.db
go run ./cmd/marketserver -db service-replica.db -read-only -addr 127.0.0.1:8081
```

The engine now takes a non-blocking OS lock before reading metadata. A writer is
exclusive; multiple read-only handles may share the same file when no writer
owns it. A live embedded writer can call `db.Backup(...)` itself without stopping
because the pager takes a consistent durable snapshot under its commit mutex.
This is point-in-time replication, not live multi-writer replication.

Phase 6 also adds prefix-compressed time-series leaves, atomic write batches,
bounded range deletion, `DROP TABLE`, crash-boundary child-process tests, and
native fuzzing. See the [validation evidence](docs/PHASE6_VALIDATION.md) and the
explicit [durability contract](docs/DURABILITY.md).

## Phase 7 range scans

Prefix compression stores one shared key prefix per leaf. The cursor rebuilds
that logical key in a reused buffer instead of allocating it on every step.
`Deref` still returns a copy that remains valid after `Next`. On the machine
recorded in [benchmarks](docs/BENCHMARKS.md), scanning the latest 252 candles
fell from 5,587 to 4,229 allocations per operation. That measurement is not a
cross-machine latency claim.

## Market intelligence quick start

Initialize the versioned market schema:

```sh
go run ./cmd/marketdb -db market.db -command init
```

Sync a five-year, multi-asset daily universe from Twelve Data. The API key is
read only from the environment and is never accepted as a command-line flag:

```sh
export TWELVE_DATA_API_KEY="your-key"
go run ./cmd/marketdb \
  -db market.db -command sync \
  -symbols AAPL,MSFT,SPY -interval 1d \
  -start-date 2021-08-29 -end-date 2026-08-27
```

The same window is already checked in as a Yahoo Chart snapshot, so the
multi-asset acceptance does not need a personal key:

```sh
go test ./market -run TestFiveYearUniverseAcceptance -count=1 -v
```

Each of AAPL, MSFT, and SPY has 1,254 NYSE sessions, full date coverage, and
1,234 feature snapshots. Details and hashes are in
[Phase 2 validation](docs/PHASE2_VALIDATION.md). That snapshot is not a live
Twelve Data sync.

The sync is restart-safe. It writes one bounded provider window at a time,
persists a checkpoint after every committed page, overlaps two periods when it
resumes to capture provider corrections, imports dividends and splits, updates
realized forecasts, recomputes affected features, and stores a quality report.
The default client budget is eight requests per minute; use
`-requests-per-minute` only when your provider plan permits a different limit.

Run the deterministic end-to-end demo. It ingests 60 candles, computes a
technical feature snapshot, runs a leakage-safe walk-forward baseline, persists
every forecast and evaluation, and prints application and storage metrics:

```sh
go run ./cmd/marketdb -db market.db -command demo -symbol AAPL -interval 1d
```

Run the Phase 3 prediction experiment on a separate synthetic dataset. It fits
and tunes persistence, moving-average, ridge, and gradient-boosted-stump models;
reserves calibration and test periods; evaluates frozen and rolling-refit
forecasts; and persists replayable models and an evidence-based model card:

```sh
go run ./cmd/marketdb -db prediction-demo.db \
  -command experiment-demo -format markdown

# Use previously ingested real data, ending at a completed daily bar.
go run ./cmd/marketdb -db market.db -command experiment \
  -symbol AAPL -interval 1d -to 1787788800 -format markdown
```

The output gives an experiment ID and fitted model run IDs. Retrieve the full
card later or use a saved model to forecast the next NYSE daily session:

```sh
go run ./cmd/marketdb -db market.db -command model-card \
  -run-id EXPERIMENT_ID -format markdown

go run ./cmd/marketdb -db market.db -command predict -run-id MODEL_RUN_ID
```

`predict` uses the latest stored completed bar; it does not download new data or
refit the model. Its interval is an empirical uncertainty estimate, not a
probability of profit. See the [prediction runbook](docs/PREDICTION.md),
[synthetic example card](docs/EXAMPLE_MODEL_CARD.md), and
[five-year AAPL validation card](docs/AAPL_MODEL_CARD.md).

Run the Phase 4 grounded analyst through the entire pipeline, without a paid API:

```sh
go run ./cmd/marketdb -db analyst-demo.db -command analysis-demo -format markdown
go run ./cmd/marketdb -db analyst-demo.db -command analyst-eval -llm-provider fixture
```

This offline demo is explicitly **not an LLM**. A real local Ollama adapter and
SEC filing-metadata adapter are included; setup, live checks, and limitations
are in the [analyst runbook](docs/ANALYST.md). The analyst ranks stored evidence,
Go validates citations and renders claims, and unsupported output fails closed.
See the [Phase 4 validation record](docs/PHASE4_VALIDATION.md) for the fixture
results and the recorded local-model score.

Import real provider-neutral CSV data:

```sh
go run ./cmd/marketdb \
  -db market.db -command import -file ./prices.csv \
  -symbol AAPL -interval 1d -source my-provider

go run ./cmd/marketdb -db market.db -command features -symbol AAPL -interval 1d
go run ./cmd/marketdb -db market.db -command backtest -symbol AAPL -interval 1d
```

CSV headers: `timestamp,open,high,low,close,adjusted_close,volume`.
`adjusted_close` is optional. Timestamps may be Unix seconds, RFC 3339, or
`YYYY-MM-DD`.

## What the market layer demonstrates

| Capability | Engineering signal |
| --- | --- |
| Composite key `(symbol, interval, timestamp)` | Index design and efficient ordered time-series access |
| Fixed-point prices and ratios | Deterministic storage without floating-point money corruption |
| Atomic, idempotent ingestion | Data-pipeline reliability and safe provider retries |
| Bounded live-provider windows + durable checkpoints | Restart-safe ETL without a workflow framework |
| Shared rate limiter, exponential backoff, `Retry-After` | Respectful and resilient external API integration |
| Persisted ingestion runs and provider-credit counts | Auditable jobs and operational observability |
| NYSE session calendar and persisted quality reports | Missing, unexpected, duplicate, invalid, and outlier detection |
| Split/dividend lineage and exact split factors | Corporate-action correctness without silent double adjustment |
| Incremental feature watermarks + correction overlap | Efficient recomputation while preserving point-in-time behavior |
| OHLCV invariants and bounded queries | Data quality and defensive API design |
| Versioned atomic migrations | Evolvable application schemas |
| Point-in-time features | Prevention of future-data leakage |
| Model runs + immutable forecast identity | Experiment tracking and reproducibility |
| Walk-forward baseline | Honest ML evaluation before sophisticated models |
| Purged chronological train/tune/calibration/test partitions | Explicit separation of model selection and final evaluation |
| Ridge + gradient-boosted stumps implemented in Go | Numerical linear algebra and learning-algorithm fundamentals |
| Frozen holdout and rolling/expanding refit comparisons | Reproducible time-series evaluation |
| Checksummed chunked model artifacts | Replayable fitted parameters within the database's 3 KB row-value limit |
| RMSE, interval coverage, width, calibration gap, regime slices | Evaluation beyond a single headline accuracy score |
| MAE, MAPE, direction accuracy, dataset hash | Measurable model performance and lineage |
| Typed `LLMClient` + input digest | Provider independence, testability, and LLM auditability |
| Evidence-ID output contract + fail-closed validation | Grounding without treating citations as proof of arbitrary prose |
| SEC publication/retrieval clocks + sanitized forecast DTOs | Temporal provenance and prevention of outcome leakage |
| Local LLM HTTP adapter + adversarial contract suite | Structured outputs, safe retries, and honest evaluation |
| Runtime counters + database page stats | Observability and benchmarkable systems work |
| Durable idempotent background jobs | Retry, cancellation, and crash-state design without a queue framework |
| Bounded REST API + request IDs + safe errors | Service contracts, overload controls, and operational debugging |
| Embedded responsive dashboard + custom Canvas chart | Full-stack delivery with a reproducible offline build |
| Prometheus metrics, JSON logs, Docker, and CI | Deployment and production-shaped engineering practices |
| OS file locks + fsynced snapshot replicas | Cross-process safety with explicit replication semantics |
| Prefix-compressed B+tree leaves | On-disk format evolution and time-series locality |
| Reused compressed-key cursor buffer | Range scans without a per-step key allocation |
| Crash failpoints + native fuzzing | Recovery reasoning beyond happy-path unit tests |
| Bounded range delete + atomic table drop | Storage maintenance with index/catalog consistency |

The quantitative model owns numeric prediction. The LLM selects and orders
versioned evidence; Go checks consistency and renders a cited, controlled-language
thesis. There is no free-form financial-claim generation in this baseline.

See [architecture](docs/ARCHITECTURE.md), [delivery roadmap](docs/ROADMAP.md),
[Twelve Data operations](docs/PROVIDER_TWELVEDATA.md),
[Phase 2 validation](docs/PHASE2_VALIDATION.md), and
[benchmark methodology](docs/BENCHMARKS.md).

---

## Database engine

`byodb` is a from-scratch database in Go, built along the progression in James
Smith's *Build Your Own Database From Scratch in Go* (2nd edition):

1. a copy-on-write B+tree with fixed 4 KiB pages;
2. a durable, single-file key/value store;
3. versioned free-page reuse;
4. atomic transactions and snapshot isolation;
5. optimistic conflict detection for concurrent writers;
6. relational tables, range scans, and secondary indexes; and
7. a recursively parsed SQL-like query language; and
8. process locks, snapshot backups, prefix-compressed leaves, and maintenance operations.

The implementation is educational but complete enough to embed in a Go
program or use through its interactive shell. It uses only the Go standard
library.

## Build and test

Go 1.22 or newer is recommended.

```sh
go test ./...
go build -o byodb-cli ./cmd/byodb
```

Run the shell against a database file:

```sh
./byodb-cli -db demo.db
```

Or execute one statement:

```sh
./byodb-cli -db demo.db -c "SELECT * FROM users;"
```

## Quick start

```sql
CREATE TABLE users (
    id int,
    name string,
    age int,
    INDEX (age, name),
    PRIMARY KEY (id),
);

INSERT INTO users (id, name, age) VALUES
    (1, 'Ada', 36),
    (2, 'Grace', 37),
    (3, 'Ken', 82);

SELECT id, name, age + 1 AS next_age
FROM users
INDEX BY age >= 30 AND age < 50
FILTER name != 'Grace'
LIMIT 20;

UPDATE users SET age = age + 1 INDEX BY id = 1;
DELETE FROM users INDEX BY id = 3;
DROP TABLE users;
```

`INDEX BY` is intentionally explicit, as in the textbook. It selects a primary
or secondary index and controls traversal direction. `FILTER` is evaluated
after indexed retrieval. `WHERE` is accepted as an alias for `FILTER`.

## Embedded API

```go
db, err := byodb.OpenDB("app.db")
if err != nil { /* handle */ }
defer db.Close()

result, err := db.Exec("SELECT * FROM users INDEX BY id >= 1 AND id < 10;")
```

Multiple statements can be grouped atomically:

```go
var tx byodb.DBTX
if err := db.Begin(&tx); err != nil { /* handle */ }

if _, err := tx.Exec("UPDATE accounts SET balance = balance - 10 INDEX BY id = 1;"); err != nil {
    db.Abort(&tx)
    // handle
}
if _, err := tx.Exec("UPDATE accounts SET balance = balance + 10 INDEX BY id = 2;"); err != nil {
    db.Abort(&tx)
    // handle
}
if err := db.Commit(&tx); err != nil {
    // ErrConflict means the transaction should be retried from Begin.
}
```

The lower-level `KV`, `KVTX`, `Record`, `TableDef`, and `Scanner` APIs are also
available for applications that do not want to use the query language.

Create and open a point-in-time read-only replica:

```go
if err := db.Backup("app-replica.db"); err != nil { /* handle */ }

replica, err := byodb.OpenDBReadOnly("app-replica.db")
if err != nil { /* handle */ }
defer replica.Close()
```

`ApplyBatch` commits up to 10,000 mixed-table mutations with one durability
boundary. `DeleteRange` removes a bounded page of matching relational rows and
returns `More`; every deleted row's secondary-index entries are maintained in
the same transaction.

## Storage and recovery design

- B+tree nodes use the byte layout from chapters 4-5 and split by encoded size;
  format v3 leaves may front-code a common key prefix once per page.
- Data pages are copy-on-write; a transaction never overwrites its live tree.
- Commits write all new pages and call `fsync` before publishing a root.
- Two checksummed meta pages alternate by version. Opening the file chooses the
  newest valid version, so a torn final meta write falls back to the prior root.
- Free pages carry the version at which they became unreachable. They are not
  reused while a transaction based on an older snapshot is active.
- Transactions keep updates in an in-memory B+tree. At commit, updates are
  checked against newer write history and then applied to the current root.

This remains a single-writer embedded database. OS locks now reject unsafe
second writers and writer/reader overlap on the same file; read-only processes
serve independent snapshot files. It does not provide live replication,
network consensus, or multi-host filesystem safety. Back up important data:
the project is intended for learning, not production workloads. The complete
boundary is documented in [DURABILITY.md](docs/DURABILITY.md).

## Project map

| File | Book concepts |
| --- | --- |
| `btree.go` | B+tree nodes, insertion, splitting, deletion, merging, iterators |
| `pager.go` | page file, crash recovery, meta pages, free-page persistence |
| `kv.go` | atomic transactions, snapshots, combined iteration, conflicts |
| `codec.go` | order-preserving integer/string and tuple encoding |
| `table.go` | schemas, records, primary keys, scans, secondary indexes |
| `parser.go` | recursive-descent SQL-like parser |
| `query.go` | expression evaluator and statement interpreter |
| `cmd/byodb` | interactive command-line shell |
| `stats.go` | page, file, transaction, and catalog metrics |
| `filelock_*.go` | cross-platform shared/exclusive database-file locks |
| `market/` | market schema, provider contract, resumable sync, quality, features, prediction experiments, model cards, LLM boundary |
| `market/provider/twelvedata` | live REST adapter, response parsing, rate limiting, retries, and contract tests |
| `market/llm/ollama` | local structured-output LLM adapter and HTTP contract tests |
| `market/source/sec` | SEC filing metadata, request policy, and source validation |
| `cmd/marketdb` | data, prediction, grounded analyst, evaluation, and replay CLI |
