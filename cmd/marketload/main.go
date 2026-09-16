// marketload is a bounded, read-only load generator for the Phase 5 HTTP API.
// It reports observed latency; it does not invent benchmark claims.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	latency time.Duration
	status  int
	bytes   int64
	err     bool
}

type report struct {
	Target          string         `json:"target"`
	Concurrency     int            `json:"concurrency"`
	ElapsedSeconds  float64        `json:"elapsed_seconds"`
	Requests        int            `json:"requests"`
	Successful      int            `json:"successful"`
	Errors          int            `json:"errors"`
	RequestsPerSec  float64        `json:"requests_per_second"`
	P50Milliseconds float64        `json:"p50_ms"`
	P95Milliseconds float64        `json:"p95_ms"`
	P99Milliseconds float64        `json:"p99_ms"`
	ResponseBytes   int64          `json:"response_bytes"`
	StatusCodes     map[string]int `json:"status_codes"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "marketload:", err)
		os.Exit(1)
	}
}

func run() error {
	target := flag.String("url", "http://127.0.0.1:8080/api/v1/overview?symbol=SYNTH&interval=1d&limit=120", "read-only HTTP target")
	concurrency := flag.Int("concurrency", 16, "parallel workers (1-512)")
	duration := flag.Duration("duration", 10*time.Second, "test duration (1s-5m)")
	maxRequests := flag.Uint64("requests", 0, "optional total request cap; zero is duration-only")
	timeout := flag.Duration("timeout", 5*time.Second, "individual request timeout")
	flag.Parse()
	parsed, err := url.Parse(*target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("url must be an HTTP(S) target without embedded credentials")
	}
	if *concurrency < 1 || *concurrency > 512 || *duration < time.Second || *duration > 5*time.Minute || *timeout < 100*time.Millisecond || *timeout > time.Minute {
		return errors.New("invalid concurrency, duration, or timeout bound")
	}
	if *maxRequests > 1_000_000 {
		return errors.New("request cap must not exceed 1000000")
	}
	transport := &http.Transport{MaxIdleConns: *concurrency, MaxIdleConnsPerHost: *concurrency, IdleConnTimeout: 30 * time.Second, DisableCompression: false}
	client := &http.Client{Transport: transport, Timeout: *timeout}
	defer transport.CloseIdleConnections()
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	scheduleCtx, cancel := context.WithTimeout(root, *duration)
	defer cancel()
	results := make(chan result, *concurrency*2)
	start := make(chan struct{})
	var sequence atomic.Uint64
	var workers sync.WaitGroup
	workers.Add(*concurrency)
	started := time.Now()
	for worker := 0; worker < *concurrency; worker++ {
		go func() {
			defer workers.Done()
			<-start
			for {
				select {
				case <-scheduleCtx.Done():
					return
				default:
				}
				n := sequence.Add(1)
				if *maxRequests > 0 && n > *maxRequests {
					return
				}
				request, err := http.NewRequestWithContext(root, http.MethodGet, parsed.String(), nil)
				if err != nil {
					results <- result{err: true}
					return
				}
				request.Header.Set("Accept", "application/json")
				at := time.Now()
				response, err := client.Do(request)
				latency := time.Since(at)
				if err != nil {
					if root.Err() == nil {
						results <- result{latency: latency, err: true}
					}
					return
				}
				bytesRead, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 16<<20))
				response.Body.Close()
				results <- result{latency: latency, status: response.StatusCode, bytes: bytesRead, err: readErr != nil}
			}
		}()
	}
	close(start)
	go func() { workers.Wait(); close(results) }()
	collected := make([]result, 0, 4096)
	for item := range results {
		collected = append(collected, item)
	}
	elapsed := time.Since(started)
	summary := summarize(parsed.String(), *concurrency, elapsed, collected)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return err
	}
	if summary.Requests == 0 || summary.Errors > 0 || summary.Successful != summary.Requests {
		return errors.New("load run included failed or non-2xx requests")
	}
	return nil
}

func summarize(target string, concurrency int, elapsed time.Duration, results []result) report {
	out := report{Target: target, Concurrency: concurrency, ElapsedSeconds: elapsed.Seconds(), Requests: len(results), StatusCodes: map[string]int{}}
	latencies := make([]time.Duration, 0, len(results))
	for _, item := range results {
		out.ResponseBytes += item.bytes
		out.StatusCodes[fmt.Sprint(item.status)]++
		if item.err || item.status < 200 || item.status >= 300 {
			out.Errors++
		} else {
			out.Successful++
		}
		latencies = append(latencies, item.latency)
	}
	if out.ElapsedSeconds > 0 {
		out.RequestsPerSec = float64(out.Requests) / out.ElapsedSeconds
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	out.P50Milliseconds = percentile(latencies, .50)
	out.P95Milliseconds = percentile(latencies, .95)
	out.P99Milliseconds = percentile(latencies, .99)
	return out
}

func percentile(values []time.Duration, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(p*float64(len(values)))) - 1
	return float64(values[index]) / float64(time.Millisecond)
}
