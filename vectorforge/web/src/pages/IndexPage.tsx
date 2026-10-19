import { useEffect, useState, type FormEvent } from 'react'
import { api, describeError, pollJob } from '../api'
import { LineChart, ListSizeChart, type Series } from '../charts'
import { bytes, num, unixTime } from '../format'
import type { Consistency, Health, Job, Stats } from '../types'
import { BenchmarkView } from './BenchmarkView'

function Card({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="card">
      <div className="card-label">{label}</div>
      <div className="card-value">{value}</div>
      {note && <div className="card-note">{note}</div>}
    </div>
  )
}

function Rebuild({ stats, health, onChange }: { stats: Stats; health: Health | null; onChange: () => void }) {
  const [nlist, setNlist] = useState('')
  const [seed, setSeed] = useState('')
  const [job, setJob] = useState<Job | null>(null)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      const j = await api.rebuild({ nlist: nlist ? Number(nlist) : undefined, seed: seed ? Number(seed) : undefined })
      setJob(j)
      await pollJob(j.job_id, setJob, 500)
    } catch (err) {
      setError(describeError(err))
    } finally {
      onChange()
    }
  }
  const auto = Math.max(1, Math.round(Math.sqrt(stats.vectors)))
  return (
    <form className="panel" onSubmit={submit}>
      <h3>Rebuild IVF</h3>
      <p className="muted">
        Retrains k-means on every stored vector and replaces centroids and postings in one transaction. Searches keep using
        the old index until the swap.
      </p>
      <div className="controls">
        <label>
          nlist
          <input type="number" min={1} max={4096} value={nlist} placeholder={`auto (${auto})`} onChange={(e) => setNlist(e.target.value)} />
        </label>
        <label>
          seed
          <input type="number" value={seed} placeholder="42" onChange={(e) => setSeed(e.target.value)} />
        </label>
        <button type="submit" disabled={!health?.mutations.enabled || job?.status === 'running' || stats.vectors === 0}>
          {job?.status === 'running' ? 'Training…' : 'Rebuild'}
        </button>
      </div>
      {job && (
        <p className={job.status === 'failed' ? 'error' : 'muted'}>
          Job {job.job_id}: {job.status}
          {job.error && ` — ${job.error}`}
        </p>
      )}
      {error && <p className="error">{error}</p>}
    </form>
  )
}

function Verify() {
  const [c, setC] = useState<Consistency | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    setError('')
    try {
      setC(await api.verify())
    } catch (err) {
      setError(describeError(err))
    } finally {
      setBusy(false)
    }
  }
  const problems = c
    ? (Object.entries(c) as [string, number | boolean][]).filter(
        ([k, v]) => typeof v === 'number' && !['documents', 'chunks', 'vectors', 'postings'].includes(k) && v > 0,
      )
    : []
  return (
    <div className="panel">
      <h3>Consistency check</h3>
      <p className="muted">
        Cross-checks documents, chunks, vectors and postings in byodb, and compares every stored vector with the in-memory
        indexes.
      </p>
      <button type="button" onClick={run} disabled={busy}>
        {busy ? 'Checking…' : 'Verify'}
      </button>
      {error && <p className="error">{error}</p>}
      {c && (
        <p className={c.ok ? 'ok' : 'error'}>
          {c.ok ? 'Consistent' : 'Inconsistent'}: {num(c.documents)} documents, {num(c.chunks)} chunks, {num(c.vectors)}{' '}
          vectors, {num(c.postings)} postings.
          {problems.map(([k, v]) => ` ${k.replace(/_/g, ' ')}: ${v}.`)}
        </p>
      )}
    </div>
  )
}

export function IndexPage({ stats, health, onChange }: { stats: Stats | null; health: Health | null; onChange: () => void }) {
  const [benchmarks, setBenchmarks] = useState<string[]>([])
  useEffect(() => {
    api.benchmarks().then(setBenchmarks, () => setBenchmarks([]))
  }, [])
  if (!stats) return <p className="muted">Loading…</p>
  const meta = stats.ivf.meta
  const history: Series[] = meta?.objective_history
    ? [{ name: 'mean cosine to assigned centroid', points: meta.objective_history.map((y, i) => ({ x: i + 1, y })) }]
    : []
  return (
    <section className="dashboard">
      <div className="cards">
        <Card label="Vectors" value={num(stats.vectors)} note={`${num(stats.documents)} documents`} />
        <Card label="Model" value={`${stats.dimension}-d ${stats.metric}`} note={stats.model_id} />
        <Card
          label="IVF"
          value={meta ? `nlist ${meta.nlist}` : 'not trained'}
          note={
            meta
              ? `generation ${meta.generation} · trained on ${num(meta.trained_vectors)} · ${num(stats.ivf.vectors_since_train)} added since`
              : 'searches use exact scan'
          }
        />
        <Card label="Default nprobe" value={String(stats.ivf.default_nprobe)} note={meta ? `${((stats.ivf.default_nprobe / meta.nlist) * 100).toFixed(1)}% of lists` : undefined} />
        <Card label="Exact index" value={bytes(stats.memory.exact_index_bytes)} note="in memory" />
        <Card label="IVF index" value={bytes(stats.memory.ivf_index_bytes)} note="in memory, incl. centroids" />
        <Card
          label="byodb file"
          value={bytes(stats.storage.file_size_bytes)}
          note={`${num(stats.storage.page_count)} pages · ${num(stats.storage.free_pages)} free`}
        />
        <Card
          label="Last startup"
          value={`${stats.startup.load_millis} ms`}
          note={`${stats.startup.ivf_loaded ? 'IVF loaded from disk' : 'no IVF on disk'} · retrained: ${stats.startup.ivf_retrained ? 'yes' : 'no'}`}
        />
      </div>

      {meta && stats.ivf.list_sizes && stats.ivf.balance && (
        <div className="columns">
          <div className="panel">
            <ListSizeChart sizes={stats.ivf.list_sizes} mean={stats.ivf.balance.mean} />
            <p className="muted small">
              min {stats.ivf.balance.min} · max {stats.ivf.balance.max} · empty {stats.ivf.balance.empty} · imbalance factor{' '}
              {stats.ivf.balance.imbalance_factor.toFixed(2)} (1.0 = perfectly even; query cost grows with it)
            </p>
          </div>
          <div className="panel">
            <LineChart
              title="k-means convergence"
              series={history}
              xLabel="Lloyd iteration"
              yLabel="objective"
              formatY={(y) => y.toFixed(2)}
            />
            <p className="muted small">
              {meta.iterations} iterations, {meta.converged ? 'converged' : 'hit the iteration cap'}, sample {num(meta.sample_size)},{' '}
              {meta.train_millis} ms, seed {meta.seed}, trained {unixTime(meta.trained_at)}
            </p>
          </div>
        </div>
      )}

      <div className="columns">
        <Rebuild stats={stats} health={health} onChange={onChange} />
        <Verify />
      </div>

      <div className="panel">
        <h3>Table rows</h3>
        <table className="compact">
          <tbody>
            {Object.entries(stats.table_rows).map(([k, v]) => (
              <tr key={k}>
                <td className="mono">{k}</td>
                <td>{num(v)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="muted small">
          Each {stats.dimension}-d vector ({stats.dimension * 4} bytes of float32) is stored as {Math.ceil(stats.dimension / 256)} rows
          of at most 256 floats, because byodb values are capped at 3000 bytes and its codec doubles escaped 0x00/0x01 bytes.
        </p>
      </div>

      <BenchmarkView names={benchmarks} />
    </section>
  )
}
