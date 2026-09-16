package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"byodb/market"
)

const DefaultVersion = "0.7.0"

//go:embed ui
var embeddedUI embed.FS

type Config struct {
	Repository     *market.Repository
	Jobs           *JobManager
	Logger         *slog.Logger
	APIToken       string
	RequestTimeout time.Duration
	MaxInFlight    int
	Version        string
	ReadOnly       bool
}

type Server struct {
	repository     *market.Repository
	jobs           *JobManager
	logger         *slog.Logger
	apiToken       string
	requestTimeout time.Duration
	version        string
	semaphore      chan struct{}
	metrics        *HTTPMetrics
	files          http.Handler
	requestSeq     atomic.Uint64
	readOnly       bool
}

func NewServer(config Config) (*Server, error) {
	if config.Repository == nil || config.Jobs == nil {
		return nil, errors.New("service server requires a repository and job manager")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 10 * time.Second
	}
	if config.RequestTimeout < time.Second || config.RequestTimeout > time.Minute {
		return nil, errors.New("request timeout must be between one second and one minute")
	}
	if config.MaxInFlight == 0 {
		config.MaxInFlight = 64
	}
	if config.MaxInFlight < 1 || config.MaxInFlight > 1024 {
		return nil, errors.New("max in-flight requests must be between 1 and 1024")
	}
	if config.Version == "" {
		config.Version = DefaultVersion
	}
	ui, err := fs.Sub(embeddedUI, "ui")
	if err != nil {
		return nil, err
	}
	return &Server{
		repository: config.Repository, jobs: config.Jobs, logger: config.Logger, apiToken: config.APIToken,
		requestTimeout: config.RequestTimeout, version: config.Version, semaphore: make(chan struct{}, config.MaxInFlight),
		metrics: newHTTPMetrics(), files: http.FileServer(http.FS(ui)), readOnly: config.ReadOnly,
	}, nil
}

func (s *Server) Handler() http.Handler { return s }

type requestIDKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	route := routeName(request.URL.Path)
	requestID := request.Header.Get("X-Request-ID")
	if !validRequestID(requestID) {
		requestID = s.newRequestID()
	}
	setSecurityHeaders(writer.Header())
	writer.Header().Set("X-Request-ID", requestID)
	request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, requestID))
	recorder := &statusRecorder{ResponseWriter: writer}
	s.metrics.active.Add(1)
	defer s.metrics.active.Add(-1)
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("http panic recovered", "request_id", requestID, "route", route, "error_code", "panic")
			if recorder.status == 0 {
				s.writeError(recorder, request, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		elapsed := time.Since(started)
		s.metrics.observe(request.Method, route, recorder.status, elapsed)
		s.logger.Info("http request", "request_id", requestID, "method", request.Method, "route", route, "status", recorder.status, "response_bytes", recorder.bytes, "duration_ms", elapsed.Milliseconds())
	}()
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	default:
		s.writeError(recorder, request, http.StatusServiceUnavailable, "server_busy", "server is at its concurrency limit")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	s.route(recorder, request.WithContext(ctx))
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	header.Set("X-Frame-Options", "DENY")
}

func routeName(path string) string {
	switch path {
	case "/healthz", "/readyz", "/metrics", "/api/v1/overview", "/api/v1/capabilities", "/api/v1/jobs", "/":
		return path
	}
	if strings.HasPrefix(path, "/api/v1/jobs/") && strings.HasSuffix(path, "/cancel") {
		return "/api/v1/jobs/{id}/cancel"
	}
	if strings.HasPrefix(path, "/api/v1/jobs/") {
		return "/api/v1/jobs/{id}"
	}
	if strings.HasPrefix(path, "/api/v1/model-cards/") {
		return "/api/v1/model-cards/{id}"
	}
	if strings.HasPrefix(path, "/api/v1/analyses/") {
		return "/api/v1/analyses/{id}"
	}
	if strings.HasPrefix(path, "/assets/") {
		return "/assets/*"
	}
	return "not_found"
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		s.health(w, r)
	case r.URL.Path == "/readyz":
		s.ready(w, r)
	case r.URL.Path == "/metrics":
		s.prometheus(w, r)
	case r.URL.Path == "/api/v1/capabilities":
		s.capabilities(w, r)
	case r.URL.Path == "/api/v1/overview":
		s.overview(w, r)
	case r.URL.Path == "/api/v1/jobs":
		s.jobsEndpoint(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/jobs/") && strings.HasSuffix(r.URL.Path, "/cancel"):
		s.cancelJob(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/jobs/"):
		s.getJob(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/model-cards/"):
		s.modelCard(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/analyses/"):
		s.analysis(w, r)
	case r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/"):
		s.static(w, r)
	default:
		s.writeError(w, r, http.StatusNotFound, "not_found", "route not found")
	}
}

func allowMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	return false
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet, http.MethodHead) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	s.writeData(w, r, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet, http.MethodHead) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	version, err := s.repository.CurrentSchemaVersion()
	if err != nil || version != market.SchemaVersion {
		s.writeError(w, r, http.StatusServiceUnavailable, "not_ready", "database schema is not ready")
		return
	}
	stats, err := s.repository.StorageStats()
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "not_ready", "database is not ready")
		return
	}
	s.writeData(w, r, http.StatusOK, map[string]any{"status": "ready", "schema_version": version, "database_version": stats.Version})
}

func (s *Server) prometheus(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.metrics.WritePrometheus(w, s.repository, s.jobs, s.version); err != nil {
		s.logger.Error("metrics collection failed", "error_code", "storage_error")
	}
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	kinds := s.jobs.Capabilities()
	if s.readOnly {
		kinds = []string{}
	}
	s.writeData(w, r, http.StatusOK, map[string]any{"version": s.version, "schema_version": market.SchemaVersion, "job_kinds": kinds, "mutations_enabled": s.apiToken != "" && !s.readOnly, "read_only": s.readOnly})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	symbol, interval, limit := r.URL.Query().Get("symbol"), r.URL.Query().Get("interval"), 120
	if symbol == "" {
		symbol = "SYNTH"
	}
	if interval == "" {
		interval = "1d"
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be an integer")
			return
		}
		limit = parsed
	}
	if limit < 21 || limit > 500 {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be between 21 and 500")
		return
	}
	result, err := buildOverview(s.repository, symbol, interval, limit)
	if err != nil {
		s.handleRepositoryError(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, result)
}

type submitJobRequest struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) jobsEndpoint(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		s.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var request submitJobRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || len(request.Payload) == 0 {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid job request")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "request must contain one JSON object")
		return
	}
	job, created, err := s.jobs.Submit(request.Kind, request.Payload, r.Header.Get("Idempotency-Key"))
	if err != nil {
		if errors.Is(err, market.ErrJobConflict) {
			s.writeError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with an earlier request")
			return
		}
		var jobErr *JobError
		if errors.As(err, &jobErr) {
			s.writeError(w, r, http.StatusBadRequest, jobErr.Code, "unsupported job kind")
			return
		}
		s.handleRepositoryError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.JobID)
	s.writeData(w, r, status, jobResponse(job))
}

func pathID(path, prefix, suffix string) (string, bool) {
	id := strings.TrimPrefix(path, prefix)
	if suffix != "" {
		if !strings.HasSuffix(id, suffix) {
			return "", false
		}
		id = strings.TrimSuffix(id, suffix)
	}
	if id == "" || len(id) > 120 || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id, ok := pathID(r.URL.Path, "/api/v1/jobs/", "")
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "not_found", "job not found")
		return
	}
	job, exists, err := s.repository.ServiceJob(id)
	if err != nil {
		s.handleRepositoryError(w, r, err)
		return
	}
	if !exists {
		s.writeError(w, r, http.StatusNotFound, "not_found", "job not found")
		return
	}
	s.writeData(w, r, http.StatusOK, jobResponse(job))
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	id, ok := pathID(r.URL.Path, "/api/v1/jobs/", "/cancel")
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "not_found", "job not found")
		return
	}
	job, err := s.jobs.Cancel(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, r, http.StatusNotFound, "not_found", "job not found")
			return
		}
		s.handleRepositoryError(w, r, err)
		return
	}
	s.writeData(w, r, http.StatusOK, jobResponse(job))
}

func (s *Server) modelCard(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id, ok := pathID(r.URL.Path, "/api/v1/model-cards/", "")
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "not_found", "model card not found")
		return
	}
	card, err := s.repository.PredictionModelCard(id)
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, "not_found", "model card not found")
		return
	}
	s.writeData(w, r, http.StatusOK, card)
}

func (s *Server) analysis(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id, ok := pathID(r.URL.Path, "/api/v1/analyses/", "")
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "not_found", "analysis not found")
		return
	}
	report, err := s.repository.LoadAnalysisReport(id)
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, "not_found", "analysis not found")
		return
	}
	s.writeData(w, r, http.StatusOK, report)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet, http.MethodHead) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	s.files.ServeHTTP(w, r)
}

func (s *Server) authorizeMutation(w http.ResponseWriter, r *http.Request) bool {
	if s.apiToken == "" {
		s.writeError(w, r, http.StatusServiceUnavailable, "mutations_disabled", "set MARKET_API_TOKEN to enable mutation endpoints")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			s.writeError(w, r, http.StatusForbidden, "origin_rejected", "request origin rejected")
			return false
		}
	}
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="marketdb"`)
		s.writeError(w, r, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return false
	}
	want, got := sha256.Sum256([]byte(s.apiToken)), sha256.Sum256([]byte(strings.TrimPrefix(value, prefix)))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="marketdb"`)
		s.writeError(w, r, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return false
	}
	return true
}

type jobView struct {
	JobID           string          `json:"job_id"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Request         json.RawMessage `json:"request"`
	Result          json.RawMessage `json:"result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	RequestDigest   string          `json:"request_digest"`
	CreatedAt       int64           `json:"created_at"`
	StartedAt       int64           `json:"started_at,omitempty"`
	FinishedAt      int64           `json:"finished_at,omitempty"`
	CancelRequested bool            `json:"cancel_requested"`
}

func jobResponse(job market.ServiceJob) jobView {
	view := jobView{JobID: job.JobID, Kind: job.Kind, Status: job.Status, Request: json.RawMessage(job.RequestJSON), ErrorCode: job.ErrorCode, RequestDigest: job.RequestDigest, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, CancelRequested: job.CancelRequested}
	if job.ResultJSON != "" {
		view.Result = json.RawMessage(job.ResultJSON)
	}
	return view
}

func (s *Server) handleRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, market.ErrInvalidMarketData) {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "request failed market-data validation")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		s.writeError(w, r, http.StatusGatewayTimeout, "request_timeout", "request deadline exceeded")
		return
	}
	s.logger.Error("repository request failed", "request_id", requestID(r.Context()), "route", routeName(r.URL.Path), "error_code", "storage_error")
	s.writeError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func (s *Server) writeData(w http.ResponseWriter, r *http.Request, status int, data any) {
	s.writeJSON(w, status, map[string]any{"data": data, "request_id": requestID(r.Context())})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "request_id": requestID(r.Context())})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.logger.Error("response encoding failed", "error_code", "encoding_error")
	}
}

func validRequestID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) newRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("request-%016x", s.requestSeq.Add(1))
}

func init() {
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
}
