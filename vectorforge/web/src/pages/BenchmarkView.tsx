import { useEffect, useState } from 'react'
import { api, describeError } from '../api'
import { LineChart, type Series } from '../charts'
import { bytes, micros, num } from '../format'

interface Latency {
  n: number
  mean_us: number
  p50_us: number
  p95_us: number
  p99_us: number
}
interface IVFRow {
  nlist: number
  nprobe: number
  recall_at_10: number
  recall_at_100?: number
  latency: Latency
  qps: number
  fraction_of_vectors_scored: number
  speedup_vs_exact: number
}
interface DatasetReport {
  dataset: { name: string; kind: string; n: number; dim: number; queries: number }
  exact: { latency: Latency; qps: number; memory_estimate_bytes: number }
  ivf_builds: { nlist: number; build_ms: number; iterations: number; balance: { imbalance_factor: number }; memory_estimate_bytes: number }[]
  ivf: IVFRow[]
  topk?: { k: number; strategy: string; latency: Latency; recall_at_k: number }[]
  concurrency?: { strategy: string; nprobe?: number; workers: number; qps: number; speedup_vs_1_worker?: number }[]
}
interface BenchReport {
  tool: 'vfbench'
  suite: string
  environment: { cpu: string; num_cpu: number; go_version: string; os: string; git_commit: string; git_dirty: boolean }
  started_at: string
  methodology: string[]
  datasets?: DatasetReport[]
  storage?: Record<string, unknown>
}
type Metrics = Record<string, number>
interface MLReport {
  experiment: string
  smoke?: boolean
  metrics: Record<string, Metrics>
  validation: Record<string, Metrics>
  comparisons: Record<string, { metric: string; mean_diff: number; ci95: [number, number]; p_diff_le_0: number; improved: number; worsened: number; unchanged: number; queries: number }>
  latency_ms: Record<string, { p50: number; p95: number; mean: number }>
  training: Record<string, { best_epoch: number; history: { epoch: number; mean_loss: number | null; validation: Metrics }[] }>
}

function DatasetView({ d }: { d: DatasetReport }) {
  const byNList = new Map<number, IVFRow[]>()
  for (const r of d.ivf) byNList.set(r.nlist, [...(byNList.get(r.nlist) ?? []), r])
  const series: Series[] = [...byNList.entries()].map(([nlist, rows]) => ({
    name: `IVF nlist=${nlist}`,
    points: rows.map((r) => ({ x: r.latency.p50_us, y: r.recall_at_10, label: `nprobe ${r.nprobe}` })),
  }))
  series.push({ name: 'exact', points: [{ x: d.exact.latency.p50_us, y: 1, label: 'brute force' }] })
  const conc = d.concurrency ?? []
  const concSeries: Series[] = ['exact', 'ivf'].map((s) => ({
    name: s === 'ivf' ? `IVF nprobe=${conc.find((c) => c.strategy === 'ivf')?.nprobe}` : 'exact',
    points: conc.filter((c) => c.strategy === s).map((c) => ({ x: c.workers, y: c.qps })),
  }))
  return (
    <div className="panel">
      <h4>
        {d.dataset.name} <span className="muted">· {num(d.dataset.n)} × {d.dataset.dim}-d · {d.dataset.queries} queries</span>
      </h4>
      <div className="columns">
        <LineChart
          title="Recall@10 vs median latency (each point is one nprobe)"
          series={series}
          xLabel="p50 latency per query"
          yLabel="recall@10"
          logX
          yDomain={[0, 1]}
          formatX={micros}
          formatY={(y) => y.toFixed(1)}
        />
        {conc.length > 0 ? (
          <LineChart title="Throughput vs goroutines" series={concSeries} xLabel="goroutines" yLabel="queries / s" formatY={(y) => num(y)} />
        ) : (
          <table className="compact">
            <thead>
              <tr>
                <th>nlist</th>
                <th>build</th>
                <th>iterations</th>
                <th>imbalance</th>
                <th>memory</th>
              </tr>
            </thead>
            <tbody>
              {d.ivf_builds.map((b) => (
                <tr key={b.nlist}>
                  <td>{b.nlist}</td>
                  <td>{num(b.build_ms)} ms</td>
                  <td>{b.iterations}</td>
                  <td>{b.balance.imbalance_factor.toFixed(2)}</td>
                  <td>{bytes(b.memory_estimate_bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      <details>
        <summary>All operating points</summary>
        <table className="compact">
          <thead>
            <tr>
              <th>nlist</th>
              <th>nprobe</th>
              <th>recall@10</th>
              {d.ivf.some((r) => r.recall_at_100) && <th>recall@100</th>}
              <th>p50</th>
              <th>p99</th>
              <th>QPS</th>
              <th>scored</th>
              <th>speedup</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td colSpan={2}>exact</td>
              <td>1.000</td>
              {d.ivf.some((r) => r.recall_at_100) && <td>1.000</td>}
              <td>{micros(d.exact.latency.p50_us)}</td>
              <td>{micros(d.exact.latency.p99_us)}</td>
              <td>{num(d.exact.qps)}</td>
              <td>100%</td>
              <td>1.0×</td>
            </tr>
            {d.ivf.map((r) => (
              <tr key={`${r.nlist}-${r.nprobe}`}>
                <td>{r.nlist}</td>
                <td>{r.nprobe}</td>
                <td>{r.recall_at_10.toFixed(3)}</td>
                {d.ivf.some((x) => x.recall_at_100) && <td>{r.recall_at_100?.toFixed(3)}</td>}
                <td>{micros(r.latency.p50_us)}</td>
                <td>{micros(r.latency.p99_us)}</td>
                <td>{num(r.qps)}</td>
                <td>{(r.fraction_of_vectors_scored * 100).toFixed(1)}%</td>
                <td>{r.speedup_vs_exact.toFixed(1)}×</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  )
}

function BenchView({ r }: { r: BenchReport }) {
  return (
    <>
      <p className="muted small">
        {r.environment.cpu} · {r.environment.num_cpu} logical CPUs · {r.environment.go_version} {r.environment.os} · commit{' '}
        {r.environment.git_commit}
        {r.environment.git_dirty && ' (uncommitted changes)'} · {new Date(r.started_at).toLocaleString()}
      </p>
      {r.datasets?.map((d) => <DatasetView key={d.dataset.name} d={d} />)}
      {r.storage && (
        <table className="compact">
          <tbody>
            {Object.entries(r.storage)
              .filter(([, v]) => typeof v !== 'object')
              .map(([k, v]) => (
                <tr key={k}>
                  <td className="mono">{k}</td>
                  <td>{typeof v === 'number' ? num(v, Number.isInteger(v) ? 0 : 2) : String(v)}</td>
                </tr>
              ))}
          </tbody>
        </table>
      )}
      <details>
        <summary>Methodology</summary>
        <ul>
          {r.methodology.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      </details>
    </>
  )
}

const ML_COLUMNS = ['nDCG@10', 'MRR@10', 'Recall@10', 'Recall@100', 'P@1']

function MLView({ r }: { r: MLReport }) {
  const systems = Object.keys(r.metrics)
  return (
    <>
      {r.smoke && <p className="error">Smoke run on a tiny subset: not a result.</p>}
      <table className="compact">
        <thead>
          <tr>
            <th>Test split ({r.metrics[systems[0]]?.queries} queries)</th>
            {ML_COLUMNS.map((c) => (
              <th key={c}>{c}</th>
            ))}
            <th>query p50</th>
          </tr>
        </thead>
        <tbody>
          {systems.map((s) => (
            <tr key={s}>
              <td>{s}</td>
              {ML_COLUMNS.map((c) => (
                <td key={c}>{r.metrics[s][c]?.toFixed(4)}</td>
              ))}
              <td>{r.latency_ms[s] ? `${r.latency_ms[s].p50.toFixed(1)} ms` : '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h4>Paired bootstrap on per-query nDCG@10</h4>
      <table className="compact">
        <thead>
          <tr>
            <th>Comparison</th>
            <th>Δ mean</th>
            <th>95% CI</th>
            <th>P(Δ ≤ 0)</th>
            <th>better / worse / same</th>
          </tr>
        </thead>
        <tbody>
          {Object.entries(r.comparisons).map(([k, c]) => (
            <tr key={k}>
              <td>{k.replace('_vs_', ' vs ')}</td>
              <td>{c.mean_diff >= 0 ? '+' : ''}{c.mean_diff.toFixed(4)}</td>
              <td>
                [{c.ci95[0].toFixed(4)}, {c.ci95[1].toFixed(4)}]
              </td>
              <td>{c.p_diff_le_0.toFixed(4)}</td>
              <td>
                {c.improved} / {c.worsened} / {c.unchanged}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {Object.keys(r.training).length > 0 && (
        <LineChart
          title="Validation nDCG@10 by epoch (epoch 0 = pretrained); test labels are never used for selection"
          series={Object.entries(r.training).map(([name, t]) => ({
            name: `${name} (selected epoch ${t.best_epoch})`,
            points: t.history.map((h) => ({ x: h.epoch, y: h.validation['nDCG@10'] })),
          }))}
          xLabel="epoch"
          yLabel="val nDCG@10"
          formatY={(y) => y.toFixed(3)}
        />
      )}
    </>
  )
}

export function BenchmarkView({ names }: { names: string[] }) {
  const [name, setName] = useState('')
  const [data, setData] = useState<unknown>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!name && names.length) setName(names.find((n) => n.includes('synthetic')) ?? names[0])
  }, [names, name])
  useEffect(() => {
    if (!name) return
    setData(null)
    setError('')
    api.benchmark(name).then(setData, (err) => setError(describeError(err)))
  }, [name])
  const bench = data && typeof data === 'object' && (data as BenchReport).tool === 'vfbench' ? (data as BenchReport) : null
  const ml = data && typeof data === 'object' && 'experiment' in data ? (data as MLReport) : null
  return (
    <div className="panel">
      <h3>Benchmarks</h3>
      {names.length === 0 ? (
        <p className="muted">
          No benchmark results found. Run <code>go run ./cmd/vfbench</code> and start the server with <code>-benchmarks vectorforge/results</code>.
        </p>
      ) : (
        <label>
          Result file
          <select value={name} onChange={(e) => setName(e.target.value)}>
            {names.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
        </label>
      )}
      {error && <p className="error">{error}</p>}
      {bench && <BenchView r={bench} />}
      {ml && <MLView r={ml} />}
      {data != null && !bench && !ml && <pre className="snippet">{JSON.stringify(data, null, 2).slice(0, 20000)}</pre>}
    </div>
  )
}
