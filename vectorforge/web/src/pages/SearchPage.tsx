import { useRef, useState, type FormEvent, type ReactNode } from 'react'
import { api, describeError } from '../api'
import { ms, num, pct } from '../format'
import type { CompareResponse, Hit, SearchRequest, SearchResponse, Stats, Strategy } from '../types'

const EXAMPLES = [
  'how are dirty pages written back before the meta page is updated',
  'set the maximum number of idle connections in the pool',
  'cancel a running command when the context expires',
  'how does the IVF index choose which lists to probe',
]

const STOPWORDS = new Set(
  'the and are for how does what when where which with from into that this then than there their they was were will can not but its use used using about after before'.split(' '),
)

function terms(query: string): string[] {
  return [...new Set(query.toLowerCase().match(/[a-z0-9_]{3,}/g) ?? [])].filter((w) => !STOPWORDS.has(w))
}

function highlight(text: string, words: string[]): ReactNode {
  if (words.length === 0) return text
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`, 'gi')
  return text.split(re).map((part, i) => (i % 2 === 1 ? <mark key={i}>{part}</mark> : part))
}

function HitCard({ hit, words, missed }: { hit: Hit; words: string[]; missed?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <li className={`hit${missed ? ' missed' : ''}`}>
      <div className="hit-head">
        <span className="rank">{hit.rank}</span>
        <span className="score" title="cosine similarity">
          <span className="score-bar">
            <span className="score-fill" style={{ width: `${Math.max(0, hit.score) * 100}%` }} />
          </span>
          {hit.score.toFixed(3)}
        </span>
        <span className="path" title={hit.doc_id}>
          {hit.path}
          <span className="lines">
            :{hit.start_line}–{hit.end_line}
          </span>
        </span>
        <span className="badge">{hit.kind}</span>
        <span className="badge subtle">{hit.source}</span>
        {missed && <span className="badge warn">missed by IVF</span>}
      </div>
      {hit.section && <div className="section">{hit.section}</div>}
      <pre className="snippet">{highlight(open && hit.text ? hit.text : hit.snippet, words)}</pre>
      {hit.text && hit.text !== hit.snippet && (
        <button type="button" className="link" onClick={() => setOpen(!open)}>
          {open ? 'Show snippet' : 'Show full chunk'}
        </button>
      )}
    </li>
  )
}

function Timing({ r }: { r: SearchResponse }) {
  return (
    <p className="timing">
      <strong>{r.strategy === 'ivf' ? `IVF (nprobe ${r.nprobe}/${r.nlist})` : 'Exact'}</strong> · embed {ms(r.timing.embed_ms)} ·
      search {ms(r.timing.search_ms)} · fetch {ms(r.timing.fetch_ms)} · total {ms(r.timing.total_ms)} · scored{' '}
      {num(r.stats.candidates_scored)} of {num(r.vectors_indexed)} vectors ({pct(r.stats.candidates_scored / Math.max(1, r.vectors_indexed))})
      {r.stats.filtered_out > 0 && ` · ${num(r.stats.filtered_out)} filtered`}
      {r.stale_hits_skipped > 0 && ` · ${r.stale_hits_skipped} stale skipped`}
    </p>
  )
}

export function SearchPage({ stats }: { stats: Stats | null }) {
  const [query, setQuery] = useState('')
  const [topK, setTopK] = useState(10)
  const [strategy, setStrategy] = useState<Strategy>('auto')
  const [nprobe, setNprobe] = useState('')
  const [kinds, setKinds] = useState<string[]>([])
  const [source, setSource] = useState('')
  const [prefix, setPrefix] = useState('')
  const [compare, setCompare] = useState(false)
  const [result, setResult] = useState<SearchResponse | null>(null)
  const [comparison, setComparison] = useState<CompareResponse | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const inflight = useRef<AbortController | null>(null)

  const run = async (q: string) => {
    if (!q.trim()) return
    inflight.current?.abort()
    const ctl = new AbortController()
    inflight.current = ctl
    const req: SearchRequest = {
      query: q,
      top_k: topK,
      strategy: compare ? 'auto' : strategy,
      nprobe: nprobe ? Number(nprobe) : undefined,
      filters: { kinds: kinds.length ? kinds : undefined, sources: source ? [source] : undefined, path_prefix: prefix || undefined },
      include_text: true,
    }
    setBusy(true)
    setError('')
    try {
      if (compare) {
        setComparison(await api.compare(req, ctl.signal))
        setResult(null)
      } else {
        setResult(await api.search(req, ctl.signal))
        setComparison(null)
      }
    } catch (err) {
      if (!(err instanceof DOMException && err.name === 'AbortError')) setError(describeError(err))
    } finally {
      if (inflight.current === ctl) setBusy(false)
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    run(query)
  }
  const words = terms((result ?? comparison)?.query ?? '')
  const allKinds = Object.keys(stats?.chunks_by_kind ?? {}).sort()
  const ivfReady = stats?.ivf.trained ?? false
  const missed = new Set(comparison?.missed_by_ivf ?? [])

  return (
    <section>
      <form className="search-form" onSubmit={submit}>
        <div className="search-row">
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Describe what you are looking for, e.g. “where is the free list persisted”"
            maxLength={2000}
            aria-label="Search query"
            autoFocus
          />
          <button type="submit" disabled={busy || !query.trim()}>
            {busy ? 'Searching…' : 'Search'}
          </button>
        </div>
        <div className="controls">
          <label>
            Top-k
            <select value={topK} onChange={(e) => setTopK(Number(e.target.value))}>
              {[5, 10, 20, 50].map((k) => (
                <option key={k}>{k}</option>
              ))}
            </select>
          </label>
          <label>
            Strategy
            <select value={strategy} disabled={compare} onChange={(e) => setStrategy(e.target.value as Strategy)}>
              <option value="auto">auto ({ivfReady ? 'IVF' : 'exact'})</option>
              <option value="exact">exact (brute force)</option>
              <option value="ivf" disabled={!ivfReady}>
                IVF-Flat
              </option>
            </select>
          </label>
          <label>
            nprobe{stats?.ivf.meta && ` (of ${stats.ivf.meta.nlist} lists)`}
            <input
              type="number"
              min={1}
              max={stats?.ivf.meta?.nlist ?? 4096}
              value={nprobe}
              placeholder={String(stats?.ivf.default_nprobe ?? 8)}
              onChange={(e) => setNprobe(e.target.value)}
              disabled={!ivfReady || (!compare && strategy === 'exact')}
            />
          </label>
          <label>
            Source
            <select value={source} onChange={(e) => setSource(e.target.value)}>
              <option value="">all</option>
              {stats?.sources.map((s) => (
                <option key={s.name}>{s.name}</option>
              ))}
            </select>
          </label>
          <label>
            Path prefix
            <input value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="e.g. net/http/" />
          </label>
          <label className="check">
            <input type="checkbox" checked={compare} disabled={!ivfReady} onChange={(e) => setCompare(e.target.checked)} />
            Compare exact vs IVF
          </label>
        </div>
        {allKinds.length > 0 && (
          <div className="kinds">
            {allKinds.map((k) => (
              <label key={k} className="check">
                <input
                  type="checkbox"
                  checked={kinds.includes(k)}
                  onChange={(e) => setKinds(e.target.checked ? [...kinds, k] : kinds.filter((x) => x !== k))}
                />
                {k} <span className="muted">({num(stats!.chunks_by_kind[k])})</span>
              </label>
            ))}
          </div>
        )}
      </form>

      {error && <p className="error">{error}</p>}

      {!result && !comparison && !error && (
        <div className="examples">
          <p className="muted">Try one of these:</p>
          {EXAMPLES.map((q) => (
            <button
              key={q}
              type="button"
              className="chip"
              onClick={() => {
                setQuery(q)
                run(q)
              }}
            >
              {q}
            </button>
          ))}
        </div>
      )}

      {result && (
        <>
          <Timing r={result} />
          {result.results.length === 0 ? (
            <p className="muted">No results{kinds.length || source || prefix ? ' match the filters' : ''}.</p>
          ) : (
            <ol className="hits">
              {result.results.map((h) => (
                <HitCard key={h.vector_id} hit={h} words={words} />
              ))}
            </ol>
          )}
        </>
      )}

      {comparison && (
        <>
          <p className="recall">
            Recall@{comparison.top_k}: <strong>{pct(comparison.recall_at_k)}</strong> of the exact top-{comparison.top_k} were
            found by IVF. Recall measures agreement with brute force, not whether the results are relevant.
          </p>
          <div className="compare">
            <div>
              <Timing r={comparison.exact} />
              <ol className="hits">
                {comparison.exact.results.map((h) => (
                  <HitCard key={h.vector_id} hit={h} words={words} missed={missed.has(h.vector_id)} />
                ))}
              </ol>
            </div>
            <div>
              <Timing r={comparison.ivf} />
              <ol className="hits">
                {comparison.ivf.results.map((h) => (
                  <HitCard key={h.vector_id} hit={h} words={words} />
                ))}
              </ol>
            </div>
          </div>
        </>
      )}
    </section>
  )
}
