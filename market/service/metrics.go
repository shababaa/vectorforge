package service

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"byodb/market"
)

type requestMetric struct {
	Count   uint64
	Seconds float64
}

type HTTPMetrics struct {
	active atomic.Int64
	mu     sync.Mutex
	values map[string]requestMetric
}

func newHTTPMetrics() *HTTPMetrics { return &HTTPMetrics{values: map[string]requestMetric{}} }

func (m *HTTPMetrics) observe(method, route string, status int, elapsed time.Duration) {
	key := method + "\x00" + route + "\x00" + strconv.Itoa(status)
	m.mu.Lock()
	value := m.values[key]
	value.Count++
	value.Seconds += elapsed.Seconds()
	m.values[key] = value
	m.mu.Unlock()
}

func prometheusLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}

func (m *HTTPMetrics) WritePrometheus(w io.Writer, repository *market.Repository, jobs *JobManager, version string) error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.values))
	values := make(map[string]requestMetric, len(m.values))
	for key, value := range m.values {
		keys = append(keys, key)
		values[key] = value
	}
	m.mu.Unlock()
	sort.Strings(keys)
	fmt.Fprintln(w, "# HELP marketdb_http_requests_total Completed HTTP requests.")
	fmt.Fprintln(w, "# TYPE marketdb_http_requests_total counter")
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		value := values[key]
		fmt.Fprintf(w, "marketdb_http_requests_total{method=\"%s\",route=\"%s\",status=\"%s\"} %d\n", prometheusLabel(parts[0]), prometheusLabel(parts[1]), parts[2], value.Count)
	}
	fmt.Fprintln(w, "# HELP marketdb_http_request_duration_seconds Request duration totals by route.")
	fmt.Fprintln(w, "# TYPE marketdb_http_request_duration_seconds summary")
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		value := values[key]
		labels := fmt.Sprintf("method=\"%s\",route=\"%s\",status=\"%s\"", prometheusLabel(parts[0]), prometheusLabel(parts[1]), parts[2])
		fmt.Fprintf(w, "marketdb_http_request_duration_seconds_sum{%s} %.9f\n", labels, value.Seconds)
		fmt.Fprintf(w, "marketdb_http_request_duration_seconds_count{%s} %d\n", labels, value.Count)
	}
	fmt.Fprintln(w, "# HELP marketdb_http_active_requests Requests currently executing.")
	fmt.Fprintln(w, "# TYPE marketdb_http_active_requests gauge")
	fmt.Fprintf(w, "marketdb_http_active_requests %d\n", m.active.Load())
	jobMetrics := jobs.Metrics()
	fmt.Fprintln(w, "# HELP marketdb_jobs_total Background job outcomes since process start.")
	fmt.Fprintln(w, "# TYPE marketdb_jobs_total counter")
	fmt.Fprintf(w, "marketdb_jobs_total{status=\"submitted\"} %d\n", jobMetrics.Submitted)
	fmt.Fprintf(w, "marketdb_jobs_total{status=\"succeeded\"} %d\n", jobMetrics.Succeeded)
	fmt.Fprintf(w, "marketdb_jobs_total{status=\"failed\"} %d\n", jobMetrics.Failed)
	fmt.Fprintf(w, "marketdb_jobs_total{status=\"cancelled\"} %d\n", jobMetrics.Cancelled)
	fmt.Fprintln(w, "# HELP marketdb_jobs_active Jobs currently executing.")
	fmt.Fprintln(w, "# TYPE marketdb_jobs_active gauge")
	fmt.Fprintf(w, "marketdb_jobs_active %d\n", jobMetrics.Active)
	stats, err := repository.StorageStats()
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "# HELP marketdb_storage_pages Database page counts.")
	fmt.Fprintln(w, "# TYPE marketdb_storage_pages gauge")
	fmt.Fprintf(w, "marketdb_storage_pages{state=\"allocated\"} %d\n", stats.PageCount)
	fmt.Fprintf(w, "marketdb_storage_pages{state=\"free\"} %d\n", stats.FreePages)
	fmt.Fprintln(w, "# HELP marketdb_build_info Build metadata.")
	fmt.Fprintln(w, "# TYPE marketdb_build_info gauge")
	fmt.Fprintf(w, "marketdb_build_info{version=\"%s\",schema=\"%d\"} 1\n", prometheusLabel(version), market.SchemaVersion)
	return nil
}
