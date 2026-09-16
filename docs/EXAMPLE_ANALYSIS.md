> Captured 2026-08-31 from the offline demo. Synthetic data and a deterministic
> fixture client—not an LLM or a market-performance claim. The IDs refer to the
> acceptance database, which is not distributed; run `analysis-demo` to create
> your own replayable report. No SEC sources were seeded in this offline demo.

# Grounded analyst: SYNTH

The supplied directional indicators disagree; no single directional conclusion is supported.

Status: completed. Provider: fixture. Model: deterministic-not-an-llm.

Analysis ID: `analysis_9eb34fdc5b683ad1bc7c50ea`

Input SHA-256: `6ba01a768a5ef9e1b53c1ebef870fb79cd45678eebda6c0e18a13a4f153762ab`

- The one-bar adjusted-close return is -0.6621%. [return\_1]
- The five-bar adjusted-close return is -0.3521%. [return\_5]
- Adjusted close is 134.145765; its 20-bar average is 132.869878. [trend\_20]
- The stored model predicts 134.044914 for session 2026-08-31 versus baseline 134.145765; interval \[133.233735, 134.861033\] has nominal coverage 90.00%, not a profit probability. [forecast\_1]
- The sample standard deviation of the last 20 log returns is 0.4372% per bar (not annualized). [volatility\_20]

## Evidence lineage

- return\_1 → feature:SYNTH:1d:1787875200:technical-v1
- return\_5 → feature:SYNTH:1d:1787875200:technical-v1
- trend\_20 → feature:SYNTH:1d:1787875200:technical-v1
- forecast\_1 → forecast:model\_620d8908082b063111846c9c:SYNTH:1d:1787875200:259200
- volatility\_20 → feature:SYNTH:1d:1787875200:technical-v1

## Limitations

- Educational evidence summary, not financial advice, a trading recommendation, or a profitability claim.
- LLM ranks evidence IDs; Go renders controlled-language claims. This does not evaluate open-ended reasoning or news sentiment.
- SEC evidence covers filing metadata only, not filing contents, earnings figures, or causal explanations.
- Daily bars become eligible on the next UTC day; this conservative rule is not an exchange-close or live-tick service.
- Candle history can be revised: event-time filtering does not reconstruct historical vendor vintages. SEC retrieval times and forecast creation times are filtered separately.
- Forecast intervals are nominal empirical coverage, not a probability of profit. No sentiment confidence score is reported.
