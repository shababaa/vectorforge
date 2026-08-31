package market

import (
	"crypto/sha256"
	"encoding/hex"
)

type AnalystEvidence struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	RecordID  string `json:"record_id"`
	Text      string `json:"text"`
	URL       string `json:"url,omitempty"`
	Direction int    `json:"direction"`
	Required  bool   `json:"required"`
}

type AnalystContext struct {
	Version          string            `json:"version"`
	Symbol           string            `json:"symbol"`
	Interval         string            `json:"interval"`
	DecisionAt       int64             `json:"decision_at"`
	BarTimestamp     int64             `json:"bar_timestamp"`
	Ready            bool              `json:"ready"`
	SupportedOutlook string            `json:"supported_outlook"`
	Warnings         []string          `json:"warnings"`
	Feature          *FeatureSnapshot  `json:"feature,omitempty"`
	Candles          []Candle          `json:"candles"`
	Forecasts        []AnalystForecast `json:"forecasts"`
	Sources          []FilingSource    `json:"sources"`
	Evidence         []AnalystEvidence `json:"evidence"`
}

// Actual/evaluation/test-metric fields cannot cross this allowlisted DTO.
type AnalystForecast struct {
	RecordID    string `json:"record_id"`
	RunID       string `json:"run_id"`
	AsOf        int64  `json:"as_of"`
	Target      int64  `json:"target"`
	Baseline    int64  `json:"baseline"`
	Predicted   int64  `json:"predicted"`
	Lower       int64  `json:"lower"`
	Upper       int64  `json:"upper"`
	CoveragePPM int64  `json:"nominal_coverage_ppm"`
	CreatedAt   int64  `json:"created_at"`
}

type AnalysisReport struct {
	Version      string            `json:"version"`
	Status       string            `json:"status"`
	Analysis     Analysis          `json:"analysis"`
	Decision     LLMResponse       `json:"decision"`
	Claims       []AnalystEvidence `json:"claims"`
	Context      AnalystContext    `json:"context"`
	SystemPrompt string            `json:"system_prompt"`
	Attempts     int               `json:"attempts"`
	Rejections   []string          `json:"rejections"`
	DurationMS   int64             `json:"duration_ms"`
	Limitations  []string          `json:"limitations"`
}

func digestBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
