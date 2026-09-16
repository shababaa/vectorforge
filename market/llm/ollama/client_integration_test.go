//go:build integration

package ollama

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"byodb"
	"byodb/market"
)

// Explicit opt-in only; never downloads a model or invokes a hosted endpoint.
func TestLiveLocalOllamaEvaluation(t *testing.T) {
	if os.Getenv("RUN_OLLAMA_INTEGRATION") != "1" {
		t.Skip("set RUN_OLLAMA_INTEGRATION=1 and OLLAMA_MODEL with a running local server")
	}
	client, err := New(Config{Model: os.Getenv("OLLAMA_MODEL")})
	if err != nil {
		t.Fatal(err)
	}
	db, err := byodb.OpenDB(filepath.Join(t.TempDir(), "eval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, _ := market.NewRepository(db)
	if err := r.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	report, err := r.EvaluateAnalyst(ctx, client, "ollama", os.Getenv("OLLAMA_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cases=%d correct=%d valid=%d rejected=%d errors=%d citation_precision=%.3f abstention_recall=%.3f", report.Cases, report.CorrectDecisions, report.ValidResponses, report.RejectedResponses, report.Errors, report.CitationPrecision, report.AbstentionRecall)
	if report.Errors > 0 || report.ValidResponses == 0 {
		t.Fatal("live adapter did not produce usable responses")
	}
}
