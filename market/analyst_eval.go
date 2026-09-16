package market

import (
	"context"
	_ "embed"
	"encoding/json"
	"time"

	"byodb"
)

//go:embed testdata/analyst_eval.json
var analystEvalJSON []byte

type AnalystEvalCase struct {
	Name            string         `json:"name"`
	ExpectedOutlook string         `json:"expected_outlook"`
	Input           AnalystContext `json:"input"`
}

type AnalystEvalResult struct {
	Name             string      `json:"name"`
	Expected         string      `json:"expected"`
	Response         LLMResponse `json:"response"`
	Rejection        string      `json:"rejection,omitempty"`
	PublishedOutlook string      `json:"published_outlook"`
	Correct          bool        `json:"correct"`
}

type AnalystEvalReport struct {
	RunID              string              `json:"run_id"`
	Version            string              `json:"version"`
	Provider           string              `json:"provider"`
	Model              string              `json:"model"`
	FixtureOnly        bool                `json:"fixture_only"`
	DatasetHash        string              `json:"dataset_hash"`
	Cases              int                 `json:"cases"`
	ValidResponses     int                 `json:"valid_responses"`
	CorrectDecisions   int                 `json:"correct_decisions"`
	RejectedResponses  int                 `json:"rejected_responses"`
	Errors             int                 `json:"errors"`
	Citations          int                 `json:"citations"`
	ValidCitations     int                 `json:"valid_citations"`
	AbstentionCases    int                 `json:"abstention_cases"`
	CorrectAbstentions int                 `json:"correct_abstentions"`
	FalseAbstentions   int                 `json:"false_abstentions"`
	DecisionAccuracy   float64             `json:"decision_accuracy"`
	CitationPrecision  float64             `json:"citation_precision"`
	AbstentionRecall   float64             `json:"abstention_recall"`
	DurationMS         int64               `json:"duration_ms"`
	Results            []AnalystEvalResult `json:"results"`
}

func AnalystEvaluationCases() ([]AnalystEvalCase, error) {
	var cases []AnalystEvalCase
	err := json.Unmarshal(analystEvalJSON, &cases)
	return cases, err
}

// One raw completion per case (no repair) exposes model errors. The published
// outlook applies the same fail-closed validator as AnalyzeReport. The labelled
// suite measures this narrow contract, NOT general financial factual accuracy.
func (r *Repository) EvaluateAnalyst(ctx context.Context, client LLMClient, provider, model string) (AnalystEvalReport, error) {
	var report AnalystEvalReport
	if _, err := NewAnalysisService(r, client, provider, model, AnalystVersion); err != nil {
		return report, err
	}
	cases, err := AnalystEvaluationCases()
	if err != nil {
		return report, err
	}
	id, err := newID("analyst_eval")
	if err != nil {
		return report, err
	}
	fixture := false
	switch client.(type) {
	case FixtureAnalyst, *FixtureAnalyst:
		fixture = true
	}
	report = AnalystEvalReport{RunID: id, Version: AnalystVersion, Provider: provider, Model: model, FixtureOnly: fixture, DatasetHash: digestBytes(analystEvalJSON), Cases: len(cases)}
	start := time.Now()
	for _, item := range cases {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		b, _ := json.Marshal(item.Input)
		response, err := client.Complete(ctx, LLMRequest{SystemPrompt: analystPrompt, InputJSON: b})
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		code := ""
		if err != nil {
			code = "completion_error"
			report.Errors++
		} else {
			code = ValidateAnalystResponse(item.Input, response)
		}
		if code == "" {
			report.ValidResponses++
		} else {
			report.RejectedResponses++
		}
		for _, id := range response.EvidenceIDs {
			report.Citations++
			for _, e := range item.Input.Evidence {
				if e.ID == id {
					report.ValidCitations++
					break
				}
			}
		}
		published := response.Outlook
		if code != "" {
			published = "abstain"
		}
		correct := code == "" && response.Outlook == item.ExpectedOutlook
		if correct {
			report.CorrectDecisions++
		}
		if item.ExpectedOutlook == "abstain" {
			report.AbstentionCases++
			if code == "" && response.Outlook == "abstain" {
				report.CorrectAbstentions++
			}
		} else if published == "abstain" {
			report.FalseAbstentions++
		}
		report.Results = append(report.Results, AnalystEvalResult{Name: item.Name, Expected: item.ExpectedOutlook, Response: response, Rejection: code, PublishedOutlook: published, Correct: correct})
	}
	report.DecisionAccuracy = float64(report.CorrectDecisions) / float64(report.Cases)
	if report.Citations > 0 {
		report.CitationPrecision = float64(report.ValidCitations) / float64(report.Citations)
	}
	if report.AbstentionCases > 0 {
		report.AbstentionRecall = float64(report.CorrectAbstentions) / float64(report.AbstentionCases)
	}
	report.DurationMS = time.Since(start).Milliseconds()
	b, err := json.Marshal(report)
	if err != nil {
		return report, err
	}
	err = r.writeTransaction(func(tx *byodb.DBTX) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return writeModelArtifact(tx, id, "analyst-eval", b)
	})
	return report, err
}

func (r *Repository) LoadAnalystEvaluation(id string) (AnalystEvalReport, error) {
	var report AnalystEvalReport
	b, err := r.readModelArtifact(id, "analyst-eval")
	if err != nil {
		return report, err
	}
	err = json.Unmarshal(b, &report)
	return report, err
}
