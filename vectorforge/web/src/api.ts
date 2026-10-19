import type {
  CompareResponse, Consistency, DocumentPage, Document, Chunk, Health, IngestRun, Job,
  SearchRequest, SearchResponse, Stats, SourceStats,
} from './types'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId?: string,
  ) {
    super(message)
  }
}

const TOKEN_KEY = 'vectorforge.token'

export const token = {
  get: () => sessionStorage.getItem(TOKEN_KEY) ?? '',
  set: (value: string) => (value ? sessionStorage.setItem(TOKEN_KEY, value) : sessionStorage.removeItem(TOKEN_KEY)),
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const bearer = token.get()
  if (bearer) headers.Authorization = `Bearer ${bearer}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  let res: Response
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError(0, 'network_error', 'VectorForge server is unreachable')
  }
  const envelope = await res.json().catch(() => null)
  if (!res.ok) {
    throw new ApiError(
      res.status,
      envelope?.error?.code ?? 'http_error',
      envelope?.error?.message ?? `${res.status} ${res.statusText}`,
      envelope?.request_id,
    )
  }
  return envelope.data as T
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v))
  const s = q.toString()
  return s ? `?${s}` : ''
}

export const api = {
  health: () => request<Health>('GET', '/health'),
  stats: () => request<Stats>('GET', '/api/v1/index/stats'),
  verify: () => request<Consistency>('GET', '/api/v1/index/verify'),
  sources: () => request<SourceStats[]>('GET', '/api/v1/sources'),
  search: (req: SearchRequest, signal?: AbortSignal) => request<SearchResponse>('POST', '/api/v1/search', req, signal),
  compare: (req: SearchRequest, signal?: AbortSignal) =>
    request<CompareResponse>('POST', '/api/v1/search/compare', req, signal),
  documents: (p: { source?: string; kind?: string; path_prefix?: string; offset: number; limit: number }) =>
    request<DocumentPage>('GET', `/api/v1/documents${query(p)}`),
  document: (id: string) =>
    request<{ document: Document; chunks: Chunk[] }>('GET', `/api/v1/documents/${encodeURIComponent(id)}`),
  deleteDocument: (id: string) => request<{ deleted: string }>('DELETE', `/api/v1/documents/${encodeURIComponent(id)}`),
  upload: (req: { path: string; content: string; kind?: string }) =>
    request<{ document: Document; created: boolean; changed: boolean }>('POST', '/api/v1/documents', req),
  ingest: (source: string, force: boolean) => request<Job>('POST', '/api/v1/ingest', { source, force }),
  rebuild: (opts: { nlist?: number; seed?: number }) => request<Job>('POST', '/api/v1/index/rebuild', opts),
  job: (id: string) => request<Job>('GET', `/api/v1/jobs/${encodeURIComponent(id)}`),
  runs: (limit = 10) => request<IngestRun[]>('GET', `/api/v1/runs${query({ limit })}`),
  benchmarks: () => request<string[]>('GET', '/api/v1/benchmarks'),
  benchmark: (name: string) =>
    request<unknown>('GET', `/api/v1/benchmarks/${name.split('/').map(encodeURIComponent).join('/')}`),
}

export async function pollJob(id: string, onUpdate: (job: Job) => void, intervalMs = 1000): Promise<Job> {
  for (;;) {
    const job = await api.job(id)
    onUpdate(job)
    if (job.status !== 'running') return job
    await new Promise((r) => setTimeout(r, intervalMs))
  }
}

export function describeError(err: unknown): string {
  if (err instanceof ApiError) return `${err.message} (${err.code}${err.status ? `, HTTP ${err.status}` : ''})`
  if (err instanceof Error) return err.message
  return String(err)
}
