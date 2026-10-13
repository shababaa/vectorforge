// Command vectorforge serves semantic search over a byodb database.
//
//	vectorforge -db vectorforge/runtime/vf.db -sources vectorforge/demo/sources.json -ui vectorforge/web/dist
//
// The database path is required and must be a dedicated VectorForge file;
// the server refuses files that hold other applications' tables, such as the
// market service's. Ingestion reads only the roots listed in -sources.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"byodb/vectorforge/api"
	"byodb/vectorforge/embed"
	"byodb/vectorforge/engine"
	"byodb/vectorforge/ingest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vectorforge:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "", "VectorForge database file (required; created if missing)")
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	embedderURL := flag.String("embedder", "http://127.0.0.1:8090", "embedding service base URL")
	sourcesPath := flag.String("sources", "", "JSON file listing ingestion sources")
	uiDir := flag.String("ui", "", "directory of the built web UI (optional)")
	benchDir := flag.String("benchmarks", "", "directory of benchmark JSON results to expose (optional)")
	readOnly := flag.Bool("read-only", false, "open the database read-only; mutations return 403")
	nprobe := flag.Int("nprobe", 8, "default IVF lists probed per query")
	nlist := flag.Int("nlist", 0, "IVF list count for rebuilds (0 = round(sqrt(N)))")
	autoTrain := flag.Int("auto-train-at", 2000, "train IVF after an ingest once this many vectors exist (0 disables)")
	wait := flag.Duration("embedder-wait", 120*time.Second, "how long to wait for the embedding service at startup")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *dbPath == "" {
		return errors.New("-db is required; use a dedicated file such as vectorforge/runtime/vf.db")
	}
	var sources []ingest.Source
	if *sourcesPath != "" {
		var err error
		if sources, err = loadSources(*sourcesPath); err != nil {
			return err
		}
	}
	token := os.Getenv("VECTORFORGE_API_TOKEN")
	if host, _, err := net.SplitHostPort(*addr); err == nil && token == "" {
		if ip := net.ParseIP(host); host == "" || (ip != nil && !ip.IsLoopback()) {
			logger.Warn("listening on a non-loopback address without VECTORFORGE_API_TOKEN; mutations are limited to loopback clients")
		}
	}

	client, err := embed.NewClient(*embedderURL, 120*time.Second)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	waitCtx, cancel := context.WithTimeout(ctx, *wait)
	info, err := client.WaitReady(waitCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("embedding service at %s: %w", *embedderURL, err)
	}
	logger.Info("embedding service ready", "model_id", info.ModelID, "dimension", info.Dimension)

	if !*readOnly {
		if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
			return err
		}
	}
	eng, err := engine.Open(ctx, engine.Config{
		Path: *dbPath, ReadOnly: *readOnly, Embedder: client, Sources: sources, Logger: logger,
		IVF: engine.IVFConfig{NList: *nlist, NProbe: *nprobe, AutoTrainAt: *autoTrain},
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	server := &http.Server{
		Addr:              *addr,
		Handler:           api.New(eng, api.Options{Token: token, UIDir: *uiDir, BenchmarksDir: *benchDir, Logger: logger}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	logger.Info("vectorforge listening", "addr", "http://"+*addr, "db", *dbPath, "read_only", *readOnly, "sources", len(sources))
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	return server.Shutdown(shutdownCtx)
}

// loadSources reads {"sources": [...]}. Relative roots are resolved against
// the file's directory and ${GOROOT} expands to the local Go installation.
func loadSources(path string) ([]ingest.Source, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Sources []ingest.Source `json:"sources"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	for i := range file.Sources {
		root := os.Expand(file.Sources[i].Root, func(name string) string {
			if name == "GOROOT" {
				return goroot()
			}
			return "${" + name + "}" // only GOROOT is expanded
		})
		if strings.Contains(root, "${") {
			return nil, fmt.Errorf("source %s: unsupported variable in root %q", file.Sources[i].Name, file.Sources[i].Root)
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(base, root)
		}
		file.Sources[i].Root = filepath.Clean(root)
	}
	return file.Sources, nil
}

func goroot() string {
	if v := os.Getenv("GOROOT"); v != "" {
		return v
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
