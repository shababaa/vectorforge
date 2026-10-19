import { useCallback, useEffect, useState } from 'react'
import { api, describeError, token } from './api'
import { num } from './format'
import { DocumentsPage } from './pages/DocumentsPage'
import { IndexPage } from './pages/IndexPage'
import { SearchPage } from './pages/SearchPage'
import type { Health, Stats } from './types'

const PAGES = { search: 'Search', documents: 'Documents', index: 'Index & benchmarks' } as const
type Page = keyof typeof PAGES

function currentPage(): Page {
  const p = window.location.hash.replace(/^#\/?/, '')
  return p in PAGES ? (p as Page) : 'search'
}

function TokenField({ health }: { health: Health | null }) {
  const [value, setValue] = useState(token.get())
  if (!health?.mutations.token_required) return null
  return (
    <label className="token">
      API token
      <input
        type="password"
        value={value}
        autoComplete="off"
        onChange={(e) => {
          setValue(e.target.value)
          token.set(e.target.value)
        }}
        placeholder="required for changes"
      />
    </label>
  )
}

export function App() {
  const [page, setPage] = useState<Page>(currentPage)
  const [health, setHealth] = useState<Health | null>(null)
  const [stats, setStats] = useState<Stats | null>(null)
  const [error, setError] = useState('')

  const refresh = useCallback(() => {
    Promise.all([api.health(), api.stats()])
      .then(([h, s]) => {
        setHealth(h)
        setStats(s)
        setError('')
      })
      .catch((err) => setError(describeError(err)))
  }, [])

  useEffect(() => {
    refresh()
    const onHash = () => setPage(currentPage())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [refresh])

  return (
    <div className="app">
      <header>
        <div className="brand">
          VectorForge <span className="muted">semantic search on byodb</span>
        </div>
        <nav>
          {(Object.keys(PAGES) as Page[]).map((p) => (
            <a key={p} href={`#/${p}`} className={p === page ? 'active' : ''}>
              {PAGES[p]}
            </a>
          ))}
        </nav>
        <div className="status">
          {health && (
            <>
              <span className={`dot ${health.status}`} title={`embedder ${health.embedder}`} />
              {num(health.vectors)} vectors · {health.ivf_trained ? 'IVF ready' : 'exact only'}
              {health.read_only && ' · read-only'}
            </>
          )}
          <TokenField health={health} />
        </div>
      </header>
      {error && <p className="error banner">{error}</p>}
      {health?.embedder === 'unavailable' && (
        <p className="error banner">The embedding service is unavailable: search and ingestion fail until it is back.</p>
      )}
      <main>
        {page === 'search' && <SearchPage stats={stats} />}
        {page === 'documents' && <DocumentsPage stats={stats} health={health} onChange={refresh} />}
        {page === 'index' && <IndexPage stats={stats} health={health} onChange={refresh} />}
      </main>
    </div>
  )
}
