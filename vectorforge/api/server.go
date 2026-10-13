// Package api exposes the VectorForge engine over HTTP/JSON.
//
// Responses use the same envelope as the market service:
// {"data": ..., "request_id": "..."} on success and
// {"error": {"code": "...", "message": "..."}, "request_id": "..."} on failure.
//
// Mutating endpoints (ingest, upload, delete, rebuild) require a bearer token
// when one is configured. Without a token they accept only loopback clients.
// Cross-origin browser requests to mutating endpoints are always rejected.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"byodb/vectorforge/embed"
	"byodb/vectorforge/engine"
	"byodb/vectorforge/store"
)

const (
	maxJSONBody   = 64 << 10
	maxUploadBody = engine.MaxUploadBytes*2 + 16<<10 // JSON escaping can double content size
)

// Options configures the HTTP server.
type Options struct {
	Token         string // bearer token for mutations; empty means loopback-only mutations
	UIDir         string // built web UI to serve at /, optional
	BenchmarksDir string // directory of benchmark JSON results, optional
	Logger        *slog.Logger
}

// Server is an http.Handler.
type Server struct {
	eng     *engine.Engine
	opts    Options
	log     *slog.Logger
	handler http.Handler
}

// New builds the route table.
func New(eng *engine.Engine, opts Options) *Server {
	s := &Server{eng: eng, opts: opts, log: opts.Logger}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/v1/sources", s.listSources)
	mux.HandleFunc("POST /api/v1/ingest", s.mutation(s.ingest))
	mux.HandleFunc("GET /api/v1/documents", s.listDocuments)
	mux.HandleFunc("POST /api/v1/documents", s.mutation(s.upload))
	mux.HandleFunc("GET /api/v1/documents/{id}", s.getDocument)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", s.mutation(s.deleteDocument))
	mux.HandleFunc("POST /api/v1/search", s.search)
	mux.HandleFunc("POST /api/v1/search/compare", s.compare)
	mux.HandleFunc("POST /api/v1/index/rebuild", s.mutation(s.rebuild))
	mux.HandleFunc("GET /api/v1/index/stats", s.stats)
	mux.HandleFunc("GET /api/v1/index/verify", s.verify)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/benchmarks", s.listBenchmarks)
	mux.HandleFunc("GET /api/v1/benchmarks/{name...}", s.getBenchmark)
	if opts.UIDir != "" {
		files := http.FileServer(http.Dir(opts.UIDir))
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" || r.URL.Path == "/index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
		}))
	}
	s.handler = s.middleware(s.routeErrors(mux))
	return s
}

// routeErrors answers unmatched API requests with JSON 404/405 responses
// instead of ServeMux's plain-text ones.
func (s *Server) routeErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		isAPI := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/health"
		// "GET /" is the UI file server, which must not answer API paths.
		if !isAPI || (pattern != "" && pattern != "GET /") {
			mux.ServeHTTP(w, r)
			return
		}
		var allowed []string
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, p := mux.Handler(probe); p != "" && p != "GET /" {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		s.writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint")
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

type requestIDKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// middleware assigns request IDs, sets security headers, recovers panics
// and logs one line per request.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			var raw [12]byte
			_, _ = rand.Read(raw[:])
			id = hex.EncodeToString(raw[:])
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		h := w.Header()
		h.Set("X-Request-ID", id)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic serving request", "request_id", id, "path", r.URL.Path, "panic", fmt.Sprint(p))
				s.writeError(rec, r, http.StatusInternalServerError, "internal_error", "internal server error")
			}
			s.log.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

func (s *Server) writeData(w http.ResponseWriter, r *http.Request, status int, data any) {
	s.writeJSON(w, status, map[string]any{"data": data, "request_id": requestID(r)})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "request_id": requestID(r)})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.log.Warn("encode response", "err", err)
	}
}

// fail maps engine errors to HTTP statuses without leaking internals.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var status *embed.StatusError
	switch {
	case errors.Is(err, engine.ErrInvalid):
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), engine.ErrInvalid.Error()+": "))
	case errors.Is(err, engine.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, engine.ErrBusy):
		s.writeError(w, r, http.StatusConflict, "busy", "another ingest, upload, delete or rebuild is running; retry later")
	case errors.Is(err, engine.ErrUntrained):
		s.writeError(w, r, http.StatusConflict, "index_not_trained", "the IVF index is not trained; use strategy=exact or POST /api/v1/index/rebuild")
	case errors.Is(err, engine.ErrReadOnly):
		s.writeError(w, r, http.StatusForbidden, "read_only", "the server was started in read-only mode")
	case errors.Is(err, engine.ErrEmbedder) && errors.As(err, &status) && status.Code < 500:
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "the embedding service rejected the input: "+status.Detail)
	case errors.Is(err, engine.ErrEmbedder):
		s.log.Warn("embedding service failure", "request_id", requestID(r), "err", err)
		s.writeError(w, r, http.StatusServiceUnavailable, "embedder_unavailable", "the embedding service is unavailable")
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		s.writeError(w, r, http.StatusGatewayTimeout, "timeout", "request deadline exceeded")
	default:
		s.log.Error("request failed", "request_id", requestID(r), "path", r.URL.Path, "err", err)
		s.writeError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// decode reads a single JSON object with unknown fields rejected. An empty
// body decodes to the zero value when allowEmpty is set.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, limit int64, dst any, allowEmpty bool) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		s.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			s.writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("request body exceeds %d bytes", limit))
		case errors.Is(err, io.EOF) && allowEmpty:
			return true
		default:
			s.writeError(w, r, http.StatusBadRequest, "invalid_json", "request body is not valid JSON for this endpoint: "+err.Error())
		}
		return false
	}
	if dec.More() {
		s.writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON object")
		return false
	}
	return true
}

// mutation wraps handlers that change data with the authorization checks.
func (s *Server) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.eng.ReadOnly() {
			s.fail(w, r, engine.ErrReadOnly)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				s.writeError(w, r, http.StatusForbidden, "origin_rejected", "cross-origin mutations are not allowed")
				return
			}
		}
		if s.opts.Token == "" {
			if !isLoopback(r.RemoteAddr) {
				s.writeError(w, r, http.StatusForbidden, "remote_mutation_rejected",
					"mutations from non-loopback clients require VECTORFORGE_API_TOKEN")
				return
			}
		} else if !s.validToken(r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="vectorforge"`)
			s.writeError(w, r, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) validToken(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, got := sha256.Sum256([]byte(s.opts.Token)), sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	info := s.eng.Info()
	embedder := "ok"
	if err := s.eng.PingEmbedder(ctx); err != nil {
		embedder = "unavailable"
	}
	st, err := s.eng.Stats()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := "ok"
	if embedder != "ok" {
		status = "degraded"
	}
	s.writeData(w, r, http.StatusOK, map[string]any{
		"status": status, "embedder": embedder, "model_id": info.ModelID, "dimension": info.Dimension,
		"documents": st.Documents, "vectors": st.Vectors, "ivf_trained": st.IVF.Trained, "read_only": st.ReadOnly,
		"mutations": map[string]bool{"token_required": s.opts.Token != "", "enabled": !st.ReadOnly},
	})
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	st, err := s.eng.Stats()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, st.Sources)
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Force  bool   `json:"force"`
	}
	if !s.decode(w, r, maxJSONBody, &req, false) {
		return
	}
	if req.Source == "" {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "source is required")
		return
	}
	job, err := s.eng.StartIngest(req.Source, req.Force)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	s.writeData(w, r, http.StatusAccepted, job)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	var req engine.UploadRequest
	if !s.decode(w, r, maxUploadBody, &req, false) {
		return
	}
	res, err := s.eng.Upload(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/v1/documents/"+res.Document.ID)
	}
	s.writeData(w, r, status, res)
}

func intParam(r *http.Request, name string, def, lo, hi int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%w: %s must be an integer between %d and %d", engine.ErrInvalid, name, lo, hi)
	}
	return v, nil
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	offset, err := intParam(r, "offset", 0, 0, 1<<30)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit, err := intParam(r, "limit", 50, 1, 500)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	docs, total, err := s.eng.Documents(q.Get("source"), q.Get("kind"), q.Get("path_prefix"), offset, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if docs == nil {
		docs = []store.Document{}
	}
	s.writeData(w, r, http.StatusOK, map[string]any{"documents": docs, "total": total, "offset": offset, "limit": limit})
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	doc, chunks, err := s.eng.Document(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, map[string]any{"document": doc, "chunks": chunks})
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.DeleteDocument(id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, map[string]string{"deleted": id})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req engine.SearchRequest
	if !s.decode(w, r, maxJSONBody, &req, false) {
		return
	}
	resp, err := s.eng.Search(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, resp)
}

func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	var req engine.SearchRequest
	if !s.decode(w, r, maxJSONBody, &req, false) {
		return
	}
	resp, err := s.eng.Compare(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, resp)
}

func (s *Server) rebuild(w http.ResponseWriter, r *http.Request) {
	var opts engine.RebuildOptions
	if !s.decode(w, r, maxJSONBody, &opts, true) {
		return
	}
	job, err := s.eng.StartRebuild(opts)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	s.writeData(w, r, http.StatusAccepted, job)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.eng.Stats()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, st)
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	c, err := s.eng.Verify()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, c)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	s.writeData(w, r, http.StatusOK, s.eng.Jobs())
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.eng.Job(r.PathValue("id"))
	if !ok {
		s.fail(w, r, engine.ErrNotFound)
		return
	}
	s.writeData(w, r, http.StatusOK, job)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 20, 1, 500)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	runs, err := s.eng.Runs(limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, runs)
}

var benchmarkSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)

// benchmarkFiles lists JSON files in BenchmarksDir and its immediate
// subdirectories as slash-separated names. Only listed names are served, so
// requests can never address anything outside this set.
func (s *Server) benchmarkFiles() ([]string, error) {
	names := []string{}
	if s.opts.BenchmarksDir == "" {
		return names, nil
	}
	var walk func(rel string, depth int) error
	walk = func(rel string, depth int) error {
		entries, err := os.ReadDir(filepath.Join(s.opts.BenchmarksDir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !benchmarkSegment.MatchString(e.Name()) {
				continue
			}
			name := e.Name()
			if rel != "" {
				name = rel + "/" + name
			}
			switch {
			case e.IsDir() && depth == 0:
				if err := walk(name, 1); err != nil {
					return err
				}
			case e.Type().IsRegular() && strings.HasSuffix(name, ".json"):
				names = append(names, name)
			}
		}
		return nil
	}
	if err := walk("", 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (s *Server) listBenchmarks(w http.ResponseWriter, r *http.Request) {
	names, err := s.benchmarkFiles()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, names)
}

func (s *Server) getBenchmark(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	names, err := s.benchmarkFiles()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if i := sort.SearchStrings(names, name); i == len(names) || names[i] != name {
		s.fail(w, r, engine.ErrNotFound)
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.opts.BenchmarksDir, filepath.FromSlash(name)))
	if errors.Is(err, os.ErrNotExist) {
		s.fail(w, r, engine.ErrNotFound)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !json.Valid(raw) {
		s.fail(w, r, fmt.Errorf("benchmark %s is not valid JSON", name))
		return
	}
	s.writeData(w, r, http.StatusOK, json.RawMessage(raw))
}
