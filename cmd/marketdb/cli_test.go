package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"byodb/market"
)

// Execute main in a subprocess so CLI errors cannot terminate the test runner.
func TestMarketDBCLIProcess(t *testing.T) {
	if os.Getenv("MARKETDB_CLI_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"marketdb"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("marketdb", flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestMarketDBPhaseThreeCLIWorkflow(t *testing.T) {
	db := filepath.Join(t.TempDir(), "cli.db")
	run := func(wantSuccess bool, args ...string) []byte {
		t.Helper()
		argv := append([]string{"-test.run=^TestMarketDBCLIProcess$", "--", "-db", db}, args...)
		cmd := exec.Command(os.Args[0], argv...)
		cmd.Env = append(os.Environ(), "MARKETDB_CLI_TEST_HELPER=1")
		output, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	init := run(true, "-command", "init")
	if !strings.Contains(string(init), `"market_schema_version": 4`) {
		t.Fatalf("schema: %s", init)
	}
	data := run(true, "-command", "experiment-demo")
	var card market.PredictionModelCard
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	if len(card.Models) != 4 || card.Symbol != "SYNTH" {
		t.Fatalf("card=%+v", card)
	}
	markdown := run(true, "-command", "model-card", "-run-id", card.ExperimentID, "-format", "markdown")
	if !strings.Contains(string(markdown), "Frozen holdout") {
		t.Fatal("missing model-card output")
	}
	forecast := run(true, "-command", "predict", "-run-id", card.Models[0].HoldoutRunID)
	var f market.Forecast
	if err := json.Unmarshal(forecast, &f); err != nil {
		t.Fatal(err)
	}
	if f.TargetTimestamp <= f.AsOfTimestamp || f.HasActual {
		t.Fatalf("forecast=%+v", f)
	}
	run(false, "-command", "predict", "-run-id", "missing")
	run(false, "-command", "experiment-demo", "-format", "unsupported")
}

func TestMarketDBPhaseFourCLIWorkflow(t *testing.T) {
	db := filepath.Join(t.TempDir(), "analyst-cli.db")
	run := func(wantSuccess bool, args ...string) []byte {
		t.Helper()
		argv := append([]string{"-test.run=^TestMarketDBCLIProcess$", "--", "-db", db}, args...)
		cmd := exec.Command(os.Args[0], argv...)
		cmd.Env = append(os.Environ(), "MARKETDB_CLI_TEST_HELPER=1")
		output, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	data := run(true, "-command", "analysis-demo")
	var report market.AnalysisReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Analysis.Provider != "fixture" || len(report.Context.Forecasts) != 1 || len(report.Claims) < 4 {
		t.Fatalf("unexpected analysis: %+v", report)
	}
	markdown := run(true, "-command", "analysis-report", "-analysis-id", report.Analysis.AnalysisID, "-format", "markdown")
	if !strings.Contains(string(markdown), "Evidence lineage") || !strings.Contains(string(markdown), "not-an-llm") {
		t.Fatal("missing audit/demo labels")
	}
	data = run(true, "-command", "analyst-eval", "-llm-provider", "fixture")
	var evaluation market.AnalystEvalReport
	if err := json.Unmarshal(data, &evaluation); err != nil {
		t.Fatal(err)
	}
	if !evaluation.FixtureOnly || evaluation.CorrectDecisions != 10 {
		t.Fatalf("unexpected evaluation: %+v", evaluation)
	}
	run(true, "-command", "analyst-eval-report", "-run-id", evaluation.RunID)
	run(false, "-command", "analysis-report", "-analysis-id", "missing")
	run(false, "-command", "analyze", "-llm-provider", "unknown")
	run(false, "-command", "analyze", "-model", "")
	run(false, "-command", "analyze", "-llm-provider", "fixture", "-interval", "1m")
	run(false, "-command", "analyze", "-timeout", "0s")
}
