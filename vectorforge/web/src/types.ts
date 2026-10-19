// Mirrors the JSON emitted by vectorforge/api and vectorforge/engine.

export interface Health {
  status: 'ok' | 'degraded'
  embedder: 'ok' | 'unavailable'
  model_id: string
  dimension: number
  documents: number
  vectors: number
  ivf_trained: boolean
  read_only: boolean
  mutations: { token_required: boolean; enabled: boolean }
}

export interface Filters {
  kinds?: string[]
  sources?: string[]
  path_prefix?: string
}

export type Strategy = 'auto' | 'exact' | 'ivf'

export interface SearchRequest {
  query: string
  top_k: number
  strategy: Strategy
  nprobe?: number
  filters: Filters
  include_text: boolean
}

export interface Hit {
  rank: number
  score: number
  vector_id: number
  doc_id: string
  source: string
  path: string
  kind: string
  chunk_ordinal: number
  section: string
  start_line: number
  end_line: number
  snippet: string
  text?: string
}

export interface SearchResponse {
  query: string
  strategy: 'exact' | 'ivf'
  top_k: number
  nprobe?: number
  nlist?: number
  model_id: string
  vectors_indexed: number
  results: Hit[]
  stats: { candidates_scored: number; lists_probed: number; filtered_out: number }
  stale_hits_skipped: number
  timing: { embed_ms: number; search_ms: number; fetch_ms: number; total_ms: number }
}

export interface CompareResponse {
  query: string
  top_k: number
  exact: SearchResponse
  ivf: SearchResponse
  recall_at_k: number
  missed_by_ivf: number[] | null
}

export interface Document {
  doc_id: string
  source: string
  path: string
  kind: string
  content_hash: string
  chunker_version: string
  model_id: string
  size_bytes: number
  chunk_count: number
  metadata?: Record<string, string>
  created_at: number
  updated_at: number
}

export interface Chunk {
  doc_id: string
  ordinal: number
  vector_id: number
  section: string
  start_line: number
  end_line: number
  tokens: number
  text: string
}

export interface DocumentPage {
  documents: Document[]
  total: number
  offset: number
  limit: number
}

export interface SourceStats {
  name: string
  description?: string
  configured: boolean
  ingestible: boolean
  documents: number
  chunks: number
}

export interface Balance {
  min: number
  max: number
  mean: number
  empty: number
  imbalance_factor: number
  total: number
}

export interface IVFMeta {
  nlist: number
  dimension: number
  model_id: string
  seed: number
  generation: number
  trained_at: number
  trained_vectors: number
  sample_size: number
  iterations: number
  converged: boolean
  objective: number
  objective_history?: number[]
  empty_reseeds: number
  train_millis: number
}

export interface Stats {
  model_id: string
  dimension: number
  metric: string
  read_only: boolean
  documents: number
  vectors: number
  chunks_by_kind: Record<string, number>
  sources: SourceStats[]
  ivf: {
    trained: boolean
    default_nprobe: number
    meta?: IVFMeta
    vectors_since_train: number
    balance?: Balance
    list_sizes?: number[]
  }
  memory: { exact_index_bytes: number; ivf_index_bytes: number }
  storage: { page_size_bytes: number; page_count: number; free_pages: number; file_size_bytes: number; table_count: number }
  table_rows: Record<string, number>
  chunker_version: string
  startup: {
    load_millis: number
    vector_load_millis: number
    ivf_load_millis: number
    documents: number
    vectors: number
    ivf_loaded: boolean
    ivf_retrained: boolean
    missing_postings_repaired: number
    stale_postings_ignored: number
    orphan_vectors: number
    interrupted_runs_marked: number
  }
  mutation_in_progress: boolean
}

export interface Consistency {
  documents: number
  chunks: number
  vectors: number
  postings: number
  chunk_count_mismatches: number
  chunks_without_document: number
  chunks_without_vector: number
  vectors_without_chunk: number
  vectors_without_posting: number
  postings_without_vector: number
  memory_mismatches: number
  ok: boolean
}

export interface IngestSummary {
  run_id: string
  source: string
  files_seen: number
  unchanged: number
  added: number
  updated: number
  deleted: number
  skipped: number
  failed: number
  chunks_embedded: number
  truncated_chunks: number
  embed_millis: number
  commit_millis: number
  total_millis: number
  skip_reasons?: Record<string, number>
  errors?: string[]
  vectors_after: number
  auto_trained: boolean
}

export type JobStatus = 'running' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'

export interface Job {
  job_id: string
  kind: 'ingest' | 'rebuild'
  source?: string
  status: JobStatus
  created_at: string
  finished_at?: string
  progress: { files_total: number; files_done: number; chunks_embedded: number; documents_committed: number }
  result?: unknown
  error?: string
}

export interface IngestRun {
  run_id: string
  status: string
  started_at: number
  finished_at: number
  summary?: IngestSummary
}
