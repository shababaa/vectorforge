package market

import "context"

type CandleFetchRequest struct {
	Symbol   string
	Interval string
	Start    int64
	End      int64
}

type ProviderRequestStats struct {
	Retries     int64 `json:"retries"`
	CreditsUsed int64 `json:"credits_used"`
}

type CandlePage struct {
	Candles    []Candle
	Instrument Instrument
	Stats      ProviderRequestStats
}

type CorporateActionPage struct {
	Actions []CorporateAction
	Stats   ProviderRequestStats
}

// MarketDataProvider is the ingestion boundary. Implementations translate one
// documented upstream API into stable domain types and never persist directly.
type MarketDataProvider interface {
	Name() string
	MaxPointsPerRequest() int
	FetchCandles(context.Context, CandleFetchRequest) (CandlePage, error)
	FetchCorporateActions(context.Context, string, int64, int64) (CorporateActionPage, error)
}
