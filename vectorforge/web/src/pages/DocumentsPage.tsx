import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, describeError, pollJob } from '../api'
import { bytes, num, unixTime } from '../format'
import type { Chunk, Document, DocumentPage, Health, IngestRun, IngestSummary, Job, Stats } from '../types'

const PAGE = 25
const MAX_UPLOAD = 256 * 1024

function JobProgress({ job }: { job: Job }) {
  const p = job.progress
  const frac = p.files_total ? p.files_done / p.files_total : 0
  const summary = job.result as IngestSummary | undefined
  return (
    <div className={`job ${job.status}`}>
      <div>
        <strong>
          {job.kind} {job.source}
        </strong>{' '}
        — {job.status}
        {job.kind === 'ingest' && ` · ${p.files_done}/${p.files_total} files · ${num(p.chunks_embedded)} chunks embedded · ${p.documents_committed} documents committed`}
      </div>
      {job.status === 'running' && job.kind === 'ingest' && (
        <progress max={1} value={frac} aria-label="ingest progress" />
      )}
      {job.error && <p className="error">{job.error}</p>}
      {summary && job.kind === 'ingest' && (
        <p className="muted">
          {summary.added} added · {summary.updated} updated · {summary.deleted} deleted · {summary.unchanged} unchanged ·{' '}
          {summary.skipped} skipped · {summary.failed} failed · embed {(summary.embed_millis / 1000).toFixed(1)} s · commit{' '}
          {(summary.commit_millis / 1000).toFixed(2)} s{summary.auto_trained && ' · IVF auto-trained'}
        </p>
      )}
    </div>
  )
}

function Sources({ stats, health, onChange }: { stats: Stats | null; health: Health | null; onChange: () => void }) {
  const [job, setJob] = useState<Job | null>(null)
  const [force, setForce] = useState(false)
  const [error, setError] = useState('')
  const [runs, setRuns] = useState<IngestRun[]>([])
  const loadRuns = useCallback(() => api.runs(8).then(setRuns).catch(() => {}), [])
  useEffect(() => {
    loadRuns()
  }, [loadRuns])

  const start = async (source: string) => {
    setError('')
    try {
      const j = await api.ingest(source, force)
      setJob(j)
      await pollJob(j.job_id, setJob)
    } catch (err) {
      setError(describeError(err))
    } finally {
      onChange()
      loadRuns()
    }
  }
  const disabled = !health?.mutations.enabled || job?.status === 'running'
  return (
    <div className="panel">
      <h3>Sources</h3>
      <p className="muted">
        Only directories configured on the server (<code>-sources</code>) can be ingested. Re-ingesting skips files whose
        content hash, chunker version and model are unchanged.
      </p>
      <table>
        <thead>
          <tr>
            <th>Source</th>
            <th>Documents</th>
            <th>Chunks</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {stats?.sources.map((s) => (
            <tr key={s.name}>
              <td>
                <strong>{s.name}</strong>
                {s.description && <div className="muted small">{s.description}</div>}
              </td>
              <td>{num(s.documents)}</td>
              <td>{num(s.chunks)}</td>
              <td>
                {s.ingestible ? (
                  <button type="button" disabled={disabled} onClick={() => start(s.name)}>
                    Ingest
                  </button>
                ) : (
                  !s.configured && <span className="muted small">not in config</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <label className="check">
        <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
        Force re-embed unchanged files
      </label>
      {error && <p className="error">{error}</p>}
      {job && <JobProgress job={job} />}
      {runs.length > 0 && (
        <details>
          <summary>Recent ingest runs</summary>
          <table>
            <thead>
              <tr>
                <th>Run</th>
                <th>Source</th>
                <th>Status</th>
                <th>Changes</th>
                <th>Chunks</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.run_id}>
                  <td className="mono small">{r.run_id}</td>
                  <td>{r.summary?.source}</td>
                  <td>{r.status}</td>
                  <td>
                    {r.summary && `+${r.summary.added} ~${r.summary.updated} −${r.summary.deleted} =${r.summary.unchanged}`}
                  </td>
                  <td>{r.summary && num(r.summary.chunks_embedded)}</td>
                  <td>{r.summary && `${(r.summary.total_millis / 1000).toFixed(1)} s`}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </div>
  )
}

function Upload({ health, onChange }: { health: Health | null; onChange: () => void }) {
  const [path, setPath] = useState('')
  const [content, setContent] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const pick = async (file: File | undefined) => {
    if (!file) return
    if (file.size > MAX_UPLOAD) {
      setError(`${file.name} is ${bytes(file.size)}; the limit is ${bytes(MAX_UPLOAD)}`)
      return
    }
    setPath(file.name)
    setContent(await file.text())
    setError('')
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    setMessage('')
    try {
      const r = await api.upload({ path, content })
      setMessage(
        r.created
          ? `Created ${r.document.path} (${r.document.chunk_count} chunks)`
          : r.changed
            ? `Updated ${r.document.path} (${r.document.chunk_count} chunks)`
            : `${r.document.path} is unchanged; nothing re-embedded`,
      )
      onChange()
    } catch (err) {
      setError(describeError(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="panel" onSubmit={submit}>
      <h3>Add a document</h3>
      <p className="muted">
        Stored under the <code>uploads</code> source. Paste text or pick a single text file; nothing else on this machine is read.
      </p>
      <label>
        Path
        <input value={path} onChange={(e) => setPath(e.target.value)} placeholder="notes/design.md" required />
      </label>
      <label>
        File
        <input type="file" accept=".md,.txt,.go,.py,.js,.ts,.tsx,.jsx" onChange={(e) => pick(e.target.files?.[0])} />
      </label>
      <textarea value={content} onChange={(e) => setContent(e.target.value)} rows={6} placeholder="Content" required />
      <button type="submit" disabled={busy || !health?.mutations.enabled || !path || !content}>
        {busy ? 'Embedding…' : 'Upload'}
      </button>
      {message && <p className="ok">{message}</p>}
      {error && <p className="error">{error}</p>}
    </form>
  )
}

function Detail({ id, onDeleted, canDelete }: { id: string; onDeleted: () => void; canDelete: boolean }) {
  const [data, setData] = useState<{ document: Document; chunks: Chunk[] } | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    setData(null)
    api.document(id).then(setData, (err) => setError(describeError(err)))
  }, [id])
  const remove = async () => {
    if (!data || !confirm(`Delete ${data.document.path} and its ${data.document.chunk_count} vectors?`)) return
    try {
      await api.deleteDocument(id)
      onDeleted()
    } catch (err) {
      setError(describeError(err))
    }
  }
  if (error) return <p className="error">{error}</p>
  if (!data) return <p className="muted">Loading…</p>
  const d = data.document
  return (
    <div className="panel detail">
      <h3 className="mono">{d.path}</h3>
      <dl className="kv">
        <dt>Document ID</dt>
        <dd className="mono small">{d.doc_id}</dd>
        <dt>Source / kind</dt>
        <dd>
          {d.source} / {d.kind}
        </dd>
        <dt>Size</dt>
        <dd>{bytes(d.size_bytes)}</dd>
        <dt>Content hash</dt>
        <dd className="mono small">{d.content_hash.slice(0, 16)}…</dd>
        <dt>Chunker / model</dt>
        <dd className="small">
          {d.chunker_version} / {d.model_id}
        </dd>
        <dt>Updated</dt>
        <dd>{unixTime(d.updated_at)}</dd>
      </dl>
      {canDelete && (
        <button type="button" className="danger" onClick={remove}>
          Delete document
        </button>
      )}
      {d.source !== 'uploads' && canDelete && (
        <p className="muted small">The file still exists in its source directory, so the next ingest of {d.source} adds it back.</p>
      )}
      <h4>{data.chunks.length} chunks</h4>
      <ol className="chunks">
        {data.chunks.map((c) => (
          <li key={c.ordinal}>
            <details>
              <summary>
                <span className="mono">
                  #{c.ordinal} lines {c.start_line}–{c.end_line}
                </span>{' '}
                {c.section && <span className="section">{c.section}</span>}{' '}
                <span className="muted small">
                  {c.tokens} tokens · vector {c.vector_id}
                </span>
              </summary>
              <pre className="snippet">{c.text}</pre>
            </details>
          </li>
        ))}
      </ol>
    </div>
  )
}

export function DocumentsPage({ stats, health, onChange }: { stats: Stats | null; health: Health | null; onChange: () => void }) {
  const [source, setSource] = useState('')
  const [kind, setKind] = useState('')
  const [prefix, setPrefix] = useState('')
  const [offset, setOffset] = useState(0)
  const [page, setPage] = useState<DocumentPage | null>(null)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<string | null>(null)

  const load = useCallback(() => {
    api
      .documents({ source, kind, path_prefix: prefix, offset, limit: PAGE })
      .then((p) => {
        setPage(p)
        setError('')
      })
      .catch((err) => setError(describeError(err)))
  }, [source, kind, prefix, offset])
  useEffect(load, [load])

  const changed = () => {
    load()
    onChange()
  }
  return (
    <section className="documents">
      <div className="columns">
        <Sources stats={stats} health={health} onChange={changed} />
        <Upload health={health} onChange={changed} />
      </div>
      <div className="panel">
        <h3>Documents {page && <span className="muted">({num(page.total)})</span>}</h3>
        <div className="controls">
          <label>
            Source
            <select value={source} onChange={(e) => (setSource(e.target.value), setOffset(0))}>
              <option value="">all</option>
              {stats?.sources.map((s) => (
                <option key={s.name}>{s.name}</option>
              ))}
            </select>
          </label>
          <label>
            Kind
            <select value={kind} onChange={(e) => (setKind(e.target.value), setOffset(0))}>
              <option value="">all</option>
              {Object.keys(stats?.chunks_by_kind ?? {}).map((k) => (
                <option key={k}>{k}</option>
              ))}
            </select>
          </label>
          <label>
            Path prefix
            <input value={prefix} onChange={(e) => (setPrefix(e.target.value), setOffset(0))} />
          </label>
        </div>
        {error && <p className="error">{error}</p>}
        <div className="columns wide-left">
          <div>
            <table className="clickable">
              <thead>
                <tr>
                  <th>Path</th>
                  <th>Kind</th>
                  <th>Chunks</th>
                  <th>Size</th>
                </tr>
              </thead>
              <tbody>
                {page?.documents.map((d) => (
                  <tr key={d.doc_id} className={selected === d.doc_id ? 'selected' : ''} onClick={() => setSelected(d.doc_id)}>
                    <td className="mono small">
                      <span className="muted">{d.source}/</span>
                      {d.path}
                    </td>
                    <td>{d.kind}</td>
                    <td>{d.chunk_count}</td>
                    <td>{bytes(d.size_bytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {page && page.total > PAGE && (
              <div className="pager">
                <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                  Previous
                </button>
                <span>
                  {offset + 1}–{Math.min(offset + PAGE, page.total)} of {page.total}
                </span>
                <button type="button" disabled={offset + PAGE >= page.total} onClick={() => setOffset(offset + PAGE)}>
                  Next
                </button>
              </div>
            )}
          </div>
          <div>
            {selected ? (
              <Detail
                id={selected}
                canDelete={!!health?.mutations.enabled}
                onDeleted={() => {
                  setSelected(null)
                  changed()
                }}
              />
            ) : (
              <p className="muted">Select a document to see its chunks.</p>
            )}
          </div>
        </div>
      </div>
    </section>
  )
}
