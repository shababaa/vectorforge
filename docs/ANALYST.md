# Phase 4: grounded analyst

This is a small, controlled-language analyst, not a new language model trained
from scratch. A locally installed model ranks database evidence through an
`LLMClient` interface. Go validates that selection and renders every market
claim from known observations. The numerical forecaster remains Phase 3's job.

There is no vector database, autonomous tool agent, fine-tuning pipeline, news
scraper, or paid API dependency. Retrieval is an indexed query over our own
database. This scope is intentional: it demonstrates interfaces, transactions,
temporal correctness, HTTP integration, security boundaries, and honest testing
without hiding the implementation behind a framework.

## Offline end-to-end demo

Use a separate database so synthetic rows cannot mix with real provider data:

```sh
go run ./cmd/marketdb -db analyst-demo.db -command analysis-demo -format markdown
go run ./cmd/marketdb -db analyst-demo.db -command analyst-eval -llm-provider fixture
```

The demo seeds 600 synthetic NYSE-session candles ending before today, runs the
Phase 3 experiment, forecasts with the tuning-selected fitted model, and stores
the analyst report. Its provider is **fixture**, and its model label explicitly
says **deterministic-not-an-llm**. Prices are deterministic; calendar dates track
the execution date. Use an empty demo DB for a fresh run on a later date.

The analysis and evaluation outputs include IDs for replay after reopening:

```sh
go run ./cmd/marketdb -db analyst-demo.db -command analysis-report \
  -analysis-id ANALYSIS_ID -format markdown
go run ./cmd/marketdb -db analyst-demo.db -command analyst-eval-report \
  -run-id EVALUATION_RUN_ID
```

These commands never contact a model provider or SEC. A successful fixture run
is evidence that the software connects end to end, not that an LLM works well.
See [the captured example report](EXAMPLE_ANALYSIS.md).

## Use a real local model

Install and start Ollama on your machine and explicitly select an installed
model that supports structured output. This project does not download models.
Check available model names with `ollama list`, then set `OLLAMA_MODEL` to the
exact tag you intend to test. Pin and record the model digest, Ollama version,
hardware, and evaluation output before making a model-quality claim.

```sh
# macOS/Linux shell; replace the tag with one installed on your machine.
export OLLAMA_MODEL="YOUR_INSTALLED_MODEL_TAG"
go run ./cmd/marketdb -db analyst-demo.db -command analyst-eval \
  -llm-provider ollama -timeout 10m

# For real data: update Phase 2 ingestion, run a Phase 3 experiment and predict
# with a fitted model ID first. Then use that same model run ID below.
go run ./cmd/marketdb -db market.db -command analyze \
  -symbol AAPL -interval 1d -run-id MODEL_RUN_ID -format markdown
```

PowerShell environment syntax is `$env:OLLAMA_MODEL = "YOUR_INSTALLED_MODEL_TAG"`.
If you omit `-run-id`, the analyst summarizes technical features and any available
filing metadata without a forecast. It never silently generates a forecast or
downloads market data. `-as-of` is the **decision time** in Unix seconds; zero
means now. Analysis currently accepts only daily bars.

The adapter uses local `POST /api/chat`, a JSON schema, non-streaming output,
temperature 0, seed 7, a 512-token output budget, and an 8,192-token context
configuration. Settings do not guarantee determinism across model/server versions.
The origin must be a numeric loopback HTTP address, default
`http://127.0.0.1:11434`. Redirects and remote endpoints are rejected. The LLM
receives no SQL, filesystem, network, or trading tools.

References: [Ollama chat API](https://docs.ollama.com/api/chat) and
[structured outputs](https://docs.ollama.com/capabilities/structured-outputs).

## Retrieve SEC filing metadata

Use an identifying User-Agent containing **your real contact email**, not the
example placeholder. No SEC API key is required.

```sh
export SEC_USER_AGENT="Your Project your-real-contact-email"
go run ./cmd/marketdb -db market.db -command filings \
  -symbol AAPL -cik 0000320193
```

The client reads one recent-submissions page and verifies its CIK and ticker
match the request. It retains at most 20 recent 10-K, 10-Q, or 8-K entries,
including amendments. There is no historical-page pagination in v1.

Stored fields: content-addressed source ID, symbol, CIK, accession, form, primary
document, canonical SEC URL, acceptance time, first-retrieved time, and SHA-256.
Only SEC archive URLs constructed from validated identifiers are accepted.
This proves which **metadata** was observed; it does not authenticate the full
filing body or imply the SEC endorses the filing's contents. The application
does not infer revenue, earnings surprises, or causal price effects from a form
being filed.

The adapter uses HTTPS, a shared five-requests-per-second ceiling per client,
a 20-second request timeout, a 4 MiB response cap, and no redirect following.
HTTP 429/5xx get at most three attempts with exponential delay and `Retry-After`.
An indicated cooldown longer than 30 seconds stops the job; a user can retry
later. HTTP 403 and other permanent errors are not retried. Transport failures
return a safe error without exposing response bodies. Run only one ingestion
process against a database and coordinate any other SEC consumers yourself.

References: [SEC submissions API](https://www.sec.gov/search-filings/edgar-application-programming-interfaces)
and [SEC fair-access guidance](https://www.sec.gov/search-filings/edgar-search-assistance/accessing-edgar-data).

## Evidence and time boundaries

One database snapshot supplies all evidence. The snapshot closes before any LLM
request, so a slow model cannot pin pages or hold a transaction open.

| Evidence | Inclusion rule | What the report can say |
| --- | --- | --- |
| Candles | Latest 21 bars with `timestamp + 86400 <= decision_at`, one source | Observed adjusted-close features |
| Features | Computed from that exact candle window | 1/5-bar return, close vs SMA20, 20-log-return sample volatility |
| Forecasts | Same origin; run completed/created by decision time; training ended before origin; forecast created by decision time; target bar not completed | Stored prediction and nominal interval, never a realized outcome |
| SEC metadata | Published and first retrieved by decision time; published within the previous 365 days | Form, accession, acceptance timestamp, canonical filing link |

At most two active forecast horizons and three filing records enter context.
Filing lookup scans at most 1,000 candidates. Missing 21-bar history, a latest
bar older than seven days, mixed providers, or an explicitly requested but
unavailable forecast causes abstention **without calling the LLM**. Missing
filings does not prevent a technical-only report; the context records a warning.

Daily timestamps are session labels. Waiting 24 hours after the label is a
conservative completion rule, not an exchange-close implementation. It can delay
close-timestamped data until the following evening. This phase does not support
intraday analysis, trading execution, or exchange-close scheduling.

Historical candle revisions remain a limitation: filtering event timestamps
does not reconstruct vendor vintages. SEC metadata retrieved today cannot be
used in yesterday's report, even if the filing was published earlier. Forecast
creation time is also distinct from its historical origin. Backtests created
today are not forecasts that were actually available in the past.

## Structured contract and defenses

The complete model output has exactly these keys:

```json
{"outlook":"mixed_signals","evidence_ids":["return_1","return_5","trend_20","forecast_1"],"abstain_reason":"none"}
```

All directional observations are mandatory citations, so the model cannot hide
negative evidence while selecting a bullish conclusion. Outlook describes the
signs of those indicators: positive, negative, mixed, or flat. It is not a buy/sell
recommendation. Other evidence may be ranked or omitted within an eight-ID limit.
The model may always abstain. Legacy `Analysis` sentiment/confidence fields are
kept at zero for schema compatibility and must not be interpreted as estimates.

- System instructions and JSON evidence use separate chat roles.
- Source identifiers and URLs are validated before storage; the model cannot fetch URLs.
- Additional fields, duplicate JSON keys, null fields, code fences, trailing JSON,
  unknown/duplicate citations, omitted counter-evidence, and inconsistent outlooks
  are rejected.
- A citation does not validate arbitrary prose. Accordingly, arbitrary generated
  prose is not part of the accepted output at all.
- Go renders numbers and source facts; user-controlled metadata is Markdown-escaped.
- Three total attempts cover retryable transport failures and validation repairs;
  permanent failures stop immediately. Only fixed error codes go into repair prompts.
- After exhaustion the stored report abstains, contains no market claims, and
  the CLI exits unsuccessfully. Ordinary insufficient-evidence abstentions are
  successful reports. Cancellation returns promptly without publishing a report.

This limits output capability instead of claiming a magic injection-proof prompt.
It does not prove the model understands finance, finds the best explanation, or
resists every future attack in a more permissive application.

## Persistence and audit

Schema v4 adds `market_filing_sources`; earlier data remains intact. The existing
checksummed chunk store is reused for `analysis-input`, `analysis-report`, and
`analyst-eval` artifact kinds. Despite the historical name `market_model_artifacts`,
it now holds both prediction and analyst artifacts keyed by their own run IDs.

The analysis row, feature snapshot, exact input bytes, accepted decision, rendered
claims, lineage IDs, base system prompt, version, attempt/rejection codes, and
elapsed time are committed atomically. Rejection codes reconstruct the fixed
repair prompt suffixes; raw rejected model/provider text is not retained. Failure
abstentions are audited too. Checksums detect accidental corruption, not malicious
rewrites by someone who has direct database access. The full evidence snapshot
remains available even if a mutable candle or feature row is later corrected.

## Evaluation and testing

```sh
go test ./...
go test -race ./...
go vet ./...
go test -cover ./...
go test ./market -run '^$' -fuzz '^FuzzDecodeLLMResponse$' -fuzztime=3s -parallel=2

# Explicit live checks; normally skipped.
RUN_OLLAMA_INTEGRATION=1 go test -tags integration ./market/llm/ollama -run TestLive -v
RUN_SEC_INTEGRATION=1 go test -tags integration ./market/source/sec -run TestLive -v
```

The ten committed evaluation cases cover positive, negative, flat and conflicting
indicators; forecast disagreement; missing/stale history; metadata limits; and
two injected-instruction examples. The injection examples intentionally include
untrusted fields that production normalization would reject, probing the model's
role separation as defense in depth.

Report denominators are explicit: correct validated decisions / all cases;
recognized raw citations / attempted citations; valid raw abstentions / labelled
abstention cases. Completion errors count against decision accuracy, not as
successful abstention. Rejected answers become published abstentions and are
reported separately, as are false abstentions on answerable cases. No-citation
precision is reported as zero, not a perfect score. There is one raw completion
per case with no repair, to expose first-attempt errors.

These are contract-compliance metrics on a small, disclosed suite. The supported
outlook is supplied to the model; this is **not an independent reasoning benchmark**.
Exact-rendering tests check factual fidelity to stored observations, not truth of
the upstream data. Do not put fixture scores on a resume as "LLM factual accuracy."
Record a real model run separately before making even a narrow model-quality claim.

## Concepts to explain in an interview

1. Why an interface plus HTTP contract tests isolates a vendor dependency.
2. How primary/secondary indexes bound retrieval and why one snapshot matters.
3. Why publication, retrieval, bar origin, and forecast creation are different clocks.
4. Why typed JSON and citations alone cannot validate a free-form factual claim.
5. How fail-closed validation, bounded retries, cancellation, and atomic audit writes interact.
6. Why a deterministic fixture score is not a measurement of LLM intelligence.

Next phase: the service/dashboard, background jobs, CI and deployment—not more
model complexity before the current behavior is understood and measured.
