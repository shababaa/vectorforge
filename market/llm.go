package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type LLMRequest struct {
	SystemPrompt string
	InputJSON    []byte
}

type LLMResponse struct {
	Thesis        string
	SentimentPPM  int64
	ConfidencePPM int64
	Evidence      []string
}

// LLMClient is intentionally provider-neutral. Adapters for hosted or local
// models can be added without coupling credentials or HTTP logic to storage.
type LLMClient interface {
	Complete(context.Context, LLMRequest) (LLMResponse, error)
}

type AnalysisService struct {
	repository    *Repository
	client        LLMClient
	provider      string
	model         string
	promptVersion string
}

func NewAnalysisService(repository *Repository, client LLMClient, provider, model, promptVersion string) (*AnalysisService, error) {
	if repository == nil || client == nil {
		return nil, fmt.Errorf("analysis service requires a repository and LLM client")
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" || strings.TrimSpace(promptVersion) == "" {
		return nil, fmt.Errorf("analysis provider, model, and prompt version are required")
	}
	return &AnalysisService{repository: repository, client: client, provider: provider, model: model, promptVersion: promptVersion}, nil
}

type analysisContext struct {
	Symbol     string          `json:"symbol"`
	Interval   string          `json:"interval"`
	AsOf       int64           `json:"as_of_unix"`
	FeatureSet FeatureSnapshot `json:"features"`
	Candles    []Candle        `json:"recent_candles"`
	Forecasts  []Forecast      `json:"quantitative_forecasts,omitempty"`
}

// Analyze builds a point-in-time context, calls the model, validates its typed
// response, and stores the result with a digest of the exact model input.
func (s *AnalysisService) Analyze(ctx context.Context, symbol, interval string, asOf int64, forecastRunID string) (Analysis, error) {
	features, err := s.repository.ComputeFeatureSnapshot(symbol, interval, asOf, DefaultFeatureSet)
	if err != nil {
		return Analysis{}, err
	}
	candles, err := s.repository.Candles(CandleQuery{Symbol: symbol, Interval: interval, From: 0, To: features.Timestamp, Limit: 10, Descending: true})
	if err != nil {
		return Analysis{}, err
	}
	var forecasts []Forecast
	if strings.TrimSpace(forecastRunID) != "" {
		forecasts, err = s.repository.ForecastsAt(forecastRunID, features.Symbol, features.Interval, features.Timestamp, 10)
		if err != nil {
			return Analysis{}, err
		}
		if len(forecasts) == 0 {
			return Analysis{}, fmt.Errorf("%w: no point-in-time forecasts found for run %q", ErrInvalidMarketData, forecastRunID)
		}
	}
	payload, err := json.Marshal(analysisContext{Symbol: features.Symbol, Interval: features.Interval, AsOf: features.Timestamp, FeatureSet: features, Candles: candles, Forecasts: forecasts})
	if err != nil {
		return Analysis{}, err
	}
	response, err := s.client.Complete(ctx, LLMRequest{SystemPrompt: "Analyze only the supplied point-in-time market evidence. Distinguish facts from inference, do not invent news, and return a concise thesis, sentiment, confidence, and supporting evidence.", InputJSON: payload})
	if err != nil {
		return Analysis{}, err
	}
	if strings.TrimSpace(response.Thesis) == "" || response.SentimentPPM < -RatioScale || response.SentimentPPM > RatioScale || response.ConfidencePPM < 0 || response.ConfidencePPM > RatioScale {
		return Analysis{}, fmt.Errorf("%w: LLM returned an invalid typed response", ErrInvalidMarketData)
	}
	evidence, err := json.Marshal(response.Evidence)
	if err != nil {
		return Analysis{}, err
	}
	digest := sha256.Sum256(payload)
	id, err := newID("analysis")
	if err != nil {
		return Analysis{}, err
	}
	analysis := Analysis{AnalysisID: id, Symbol: features.Symbol, AsOfTimestamp: features.Timestamp, Provider: s.provider, Model: s.model, PromptVersion: s.promptVersion, InputDigest: hex.EncodeToString(digest[:]), Thesis: response.Thesis, SentimentPPM: response.SentimentPPM, ConfidencePPM: response.ConfidencePPM, EvidenceJSON: string(evidence), ForecastRunID: forecastRunID, CreatedAt: s.repository.now().UTC().Unix()}
	if _, err := s.repository.db.Insert(tableAnalyses, analysisRecord(analysis)); err != nil {
		return Analysis{}, err
	}
	return analysis, nil
}
