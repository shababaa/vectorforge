// marketserver runs the embedded database as a single-process HTTP service.
// It is intentionally one binary: the database does not yet provide safe
// inter-process file locking, so horizontal replicas are outside Phase 5.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"byodb"
	"byodb/market"
	"byodb/market/llm/ollama"
	"byodb/market/provider/twelvedata"
	marketservice "byodb/market/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "marketserver:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "market-service.db", "database file")
	address := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	seedDemo := flag.Bool("seed-demo", false, "seed the repeatable synthetic end-to-end demonstration")
	workers := flag.Int("workers", 1, "background workers (1-4)")
	requestTimeout := flag.Duration("request-timeout", 10*time.Second, "per-request API timeout")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown budget")
	maxInFlight := flag.Int("max-in-flight", 64, "maximum concurrent HTTP requests")
	requestsPerMinute := flag.Int("provider-rpm", envInt("TWELVE_DATA_REQUESTS_PER_MINUTE", 8), "Twelve Data request budget per minute")
	logLevel := flag.String("log-level", "info", "debug, info, warn, or error")
	flag.Parse()
	if *shutdownTimeout < time.Second || *shutdownTimeout > time.Minute {
		return errors.New("shutdown timeout must be between one second and one minute")
	}
	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	db, err := byodb.OpenDB(*dbPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	repository, err := market.NewRepository(db)
	if err != nil {
		return err
	}
	if err := repository.EnsureSchema(); err != nil {
		return err
	}

	manager, err := marketservice.NewJobManager(repository, logger, *workers)
	if err != nil {
		return err
	}
	dependencies := marketservice.JobDependencies{Repository: repository}
	if key := strings.TrimSpace(os.Getenv("TWELVE_DATA_API_KEY")); key != "" {
		provider, err := twelvedata.New(twelvedata.Config{APIKey: key, RequestsPerMinute: *requestsPerMinute})
		if err != nil {
			return err
		}
		dependencies.Sync, err = market.NewSyncService(repository, provider)
		if err != nil {
			return err
		}
	}
	if model := strings.TrimSpace(os.Getenv("OLLAMA_MODEL")); model != "" {
		client, err := ollama.New(ollama.Config{BaseURL: strings.TrimSpace(os.Getenv("OLLAMA_URL")), Model: model})
		if err != nil {
			return err
		}
		dependencies.Analysis, err = market.NewAnalysisService(repository, client, "ollama", model, market.AnalystVersion)
		if err != nil {
			return err
		}
	}
	if err := marketservice.RegisterBuiltinJobs(manager, dependencies); err != nil {
		return err
	}
	if *seedDemo {
		logger.Info("seeding synthetic demonstration", "database", *dbPath)
		manifest, err := repository.SeedServiceDemo(context.Background(), time.Now().UTC())
		if err != nil {
			return err
		}
		logger.Info("synthetic demonstration ready", "experiment_id", manifest.ExperimentID, "analysis_id", manifest.AnalysisID)
	}

	root, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	if _, err := manager.Start(root); err != nil {
		return err
	}
	service, err := marketservice.NewServer(marketservice.Config{Repository: repository, Jobs: manager, Logger: logger, APIToken: strings.TrimSpace(os.Getenv("MARKET_API_TOKEN")), RequestTimeout: *requestTimeout, MaxInFlight: *maxInFlight})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
		ErrorLog: log.New(slogWriter{logger}, "", 0),
	}
	serveError := make(chan error, 1)
	go func() { serveError <- httpServer.Serve(listener) }()
	logger.Info("market service started", "address", listener.Addr().String(), "database", *dbPath, "schema_version", market.SchemaVersion, "mutations_enabled", strings.TrimSpace(os.Getenv("MARKET_API_TOKEN")) != "", "job_kinds", manager.Capabilities())
	select {
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-root.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()
	logger.Info("market service stopping")
	httpErr := httpServer.Shutdown(shutdown)
	jobErr := manager.Shutdown(shutdown)
	dbErr := db.Close()
	closed = dbErr == nil
	return errors.Join(httpErr, jobErr, dbErr)
}

type slogWriter struct{ logger *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	w.logger.Warn("http server", "message", strings.TrimSpace(string(p)))
	return len(p), nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("log level must be debug, info, warn, or error")
	}
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
