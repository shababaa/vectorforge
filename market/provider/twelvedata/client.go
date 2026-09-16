// Package twelvedata implements the official Twelve Data REST API boundary.
package twelvedata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"byodb/market"
)

const (
	defaultBaseURL = "https://api.twelvedata.com"
	maxBodyBytes   = 32 << 20
)

type SleepFunc func(context.Context, time.Duration) error

type Config struct {
	APIKey            string
	BaseURL           string
	HTTPClient        *http.Client
	RequestsPerMinute int
	MaxAttempts       int
	Limiter           Limiter
	Sleep             SleepFunc
}

type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	maxAttempts int
	limiter     Limiter
	sleep       SleepFunc
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("Twelve Data API key is required")
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid Twelve Data base URL")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if config.RequestsPerMinute <= 0 {
		config.RequestsPerMinute = 8
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 4
	}
	if config.Limiter == nil {
		config.Limiter = newSpacedLimiter(config.RequestsPerMinute, time.Minute)
	}
	if config.Sleep == nil {
		config.Sleep = sleepContext
	}
	return &Client{apiKey: strings.TrimSpace(config.APIKey), baseURL: strings.TrimRight(config.BaseURL, "/"), httpClient: config.HTTPClient, maxAttempts: config.MaxAttempts, limiter: config.Limiter, sleep: config.Sleep}, nil
}

func (c *Client) Name() string             { return "twelvedata" }
func (c *Client) MaxPointsPerRequest() int { return 5_000 }

type responseMeta struct {
	Symbol           string `json:"symbol"`
	Currency         string `json:"currency"`
	Exchange         string `json:"exchange"`
	ExchangeTimezone string `json:"exchange_timezone"`
	Type             string `json:"type"`
}

type timeSeriesResponse struct {
	Meta   responseMeta `json:"meta"`
	Values []struct {
		Datetime string `json:"datetime"`
		Open     string `json:"open"`
		High     string `json:"high"`
		Low      string `json:"low"`
		Close    string `json:"close"`
		Volume   string `json:"volume"`
	} `json:"values"`
	Status string `json:"status"`
}

func (c *Client) FetchCandles(ctx context.Context, request market.CandleFetchRequest) (market.CandlePage, error) {
	providerInterval, err := providerInterval(request.Interval)
	if err != nil {
		return market.CandlePage{}, err
	}
	query := url.Values{"symbol": {request.Symbol}, "interval": {providerInterval}, "order": {"ASC"}, "timezone": {"UTC"}, "dp": {"8"}, "outputsize": {"5000"}}
	query.Set("start_date", formatBoundary(request.Interval, request.Start))
	period, err := providerPeriodSeconds(request.Interval)
	if err != nil {
		return market.CandlePage{}, err
	}
	if request.End > math.MaxInt64-period {
		return market.CandlePage{}, errors.New("Twelve Data request end timestamp overflows provider boundary")
	}
	// Twelve Data historical ranges treat end_date as exclusive. Request one
	// provider period beyond our inclusive boundary; SyncService still filters
	// every returned candle back to the caller's exact range.
	query.Set("end_date", formatBoundary(request.Interval, request.End+period))
	var response timeSeriesResponse
	stats, err := c.getJSON(ctx, "/time_series", query, &response)
	if err != nil {
		return market.CandlePage{}, err
	}
	page := market.CandlePage{Stats: stats, Instrument: market.Instrument{Symbol: response.Meta.Symbol, Name: response.Meta.Symbol, AssetType: response.Meta.Type, Exchange: response.Meta.Exchange, Currency: response.Meta.Currency, Active: true}}
	page.Candles = make([]market.Candle, 0, len(response.Values))
	for i, value := range response.Values {
		candle, err := parseCandle(request, value.Datetime, value.Open, value.High, value.Low, value.Close, value.Volume)
		if err != nil {
			return market.CandlePage{}, fmt.Errorf("Twelve Data candle %d: %w", i, err)
		}
		page.Candles = append(page.Candles, candle)
	}
	return page, nil
}

type splitsResponse struct {
	Meta   responseMeta `json:"meta"`
	Splits []struct {
		Date        string  `json:"date"`
		Description string  `json:"description"`
		Ratio       float64 `json:"ratio"`
		FromFactor  int64   `json:"from_factor"`
		ToFactor    int64   `json:"to_factor"`
	} `json:"splits"`
}

type dividendsResponse struct {
	Meta      responseMeta `json:"meta"`
	Dividends []struct {
		ExDate string  `json:"ex_date"`
		Amount float64 `json:"amount"`
	} `json:"dividends"`
}

func (c *Client) FetchCorporateActions(ctx context.Context, symbol string, from, to int64) (market.CorporateActionPage, error) {
	query := url.Values{"symbol": {symbol}, "start_date": {time.Unix(from, 0).UTC().Format("2006-01-02")}, "end_date": {time.Unix(to, 0).UTC().Format("2006-01-02")}}
	var splits splitsResponse
	splitStats, err := c.getJSON(ctx, "/splits", query, &splits)
	if err != nil {
		return market.CorporateActionPage{}, err
	}
	var dividends dividendsResponse
	dividendStats, err := c.getJSON(ctx, "/dividends", query, &dividends)
	if err != nil {
		return market.CorporateActionPage{}, err
	}
	page := market.CorporateActionPage{Stats: market.ProviderRequestStats{Retries: splitStats.Retries + dividendStats.Retries, CreditsUsed: splitStats.CreditsUsed + dividendStats.CreditsUsed}}
	for _, split := range splits.Splits {
		exDate, err := parseDate(split.Date)
		if err != nil {
			return market.CorporateActionPage{}, err
		}
		digest := digestJSON(split)
		page.Actions = append(page.Actions, market.CorporateAction{Symbol: symbol, ActionType: market.CorporateActionSplit, ExDate: exDate, Source: c.Name(), SplitFrom: split.FromFactor, SplitTo: split.ToFactor, RawDigest: digest})
	}
	currency := dividends.Meta.Currency
	for _, dividend := range dividends.Dividends {
		exDate, err := parseDate(dividend.ExDate)
		if err != nil {
			return market.CorporateActionPage{}, err
		}
		amount, err := market.ScalePrice(dividend.Amount)
		if err != nil {
			return market.CorporateActionPage{}, err
		}
		page.Actions = append(page.Actions, market.CorporateAction{Symbol: symbol, ActionType: market.CorporateActionDividend, ExDate: exDate, Source: c.Name(), Amount: amount, Currency: currency, RawDigest: digestJSON(dividend)})
	}
	return page, nil
}

func providerInterval(interval string) (string, error) {
	values := map[string]string{"1m": "1min", "5m": "5min", "15m": "15min", "30m": "30min", "1h": "1h", "4h": "4h", "1d": "1day", "1wk": "1week", "1mo": "1month"}
	value, ok := values[interval]
	if !ok {
		return "", fmt.Errorf("unsupported Twelve Data interval %q", interval)
	}
	return value, nil
}

func providerPeriodSeconds(interval string) (int64, error) {
	values := map[string]int64{"1m": 60, "5m": 300, "15m": 900, "30m": 1800, "1h": 3600, "4h": 14400, "1d": 86400, "1wk": 604800, "1mo": 2678400}
	value, ok := values[interval]
	if !ok {
		return 0, fmt.Errorf("unsupported Twelve Data interval %q", interval)
	}
	return value, nil
}

func formatBoundary(interval string, timestamp int64) string {
	t := time.Unix(timestamp, 0).UTC()
	if interval == "1d" || interval == "1wk" || interval == "1mo" {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

func parseCandle(request market.CandleFetchRequest, datetime, openText, highText, lowText, closeText, volumeText string) (market.Candle, error) {
	timestamp, err := parseProviderTime(datetime)
	if err != nil {
		return market.Candle{}, err
	}
	parsePrice := func(name, text string) (int64, error) {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid %s %q", name, text)
		}
		return market.ScalePrice(value)
	}
	open, err := parsePrice("open", openText)
	if err != nil {
		return market.Candle{}, err
	}
	high, err := parsePrice("high", highText)
	if err != nil {
		return market.Candle{}, err
	}
	low, err := parsePrice("low", lowText)
	if err != nil {
		return market.Candle{}, err
	}
	closeValue, err := parsePrice("close", closeText)
	if err != nil {
		return market.Candle{}, err
	}
	var volume int64
	if strings.TrimSpace(volumeText) != "" {
		parsed, err := strconv.ParseFloat(volumeText, 64)
		if err != nil || parsed < 0 {
			return market.Candle{}, fmt.Errorf("invalid volume %q", volumeText)
		}
		volume = int64(parsed)
	}
	return market.Candle{Symbol: request.Symbol, Interval: request.Interval, Timestamp: timestamp, Open: open, High: high, Low: low, Close: closeValue, AdjustedClose: closeValue, Volume: volume, Source: "twelvedata"}, nil
}

func parseProviderTime(value string) (int64, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid provider datetime %q", value)
}

func parseDate(value string) (int64, error) {
	parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
	if err != nil {
		return 0, fmt.Errorf("invalid corporate-action date %q", value)
	}
	return parsed.Unix(), nil
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type errorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, destination any) (market.ProviderRequestStats, error) {
	query = cloneValues(query)
	query.Set("apikey", c.apiKey)
	endpoint := c.baseURL + path + "?" + query.Encode()
	stats := market.ProviderRequestStats{}
	var lastStatus int
	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return stats, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return stats, err
		}
		request.Header.Set("Accept", "application/json")
		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			if attempt+1 == c.maxAttempts {
				return stats, errors.New("Twelve Data request failed after retries")
			}
			stats.Retries++
			if err := c.sleep(ctx, backoff(attempt, 0)); err != nil {
				return stats, err
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
		response.Body.Close()
		stats.CreditsUsed += headerInt(response.Header.Get("api-credits-used"), 1)
		if readErr != nil {
			return stats, fmt.Errorf("read Twelve Data response: %w", readErr)
		}
		if len(body) > maxBodyBytes {
			return stats, errors.New("Twelve Data response exceeds 32 MiB limit")
		}
		lastStatus = response.StatusCode
		var envelope errorEnvelope
		_ = json.Unmarshal(body, &envelope)
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 || (envelope.Status == "error" && envelope.Code == http.StatusTooManyRequests)
		if retryable && attempt+1 < c.maxAttempts {
			stats.Retries++
			if err := c.sleep(ctx, backoff(attempt, retryAfter(response.Header.Get("Retry-After")))); err != nil {
				return stats, err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Status == "error" {
			message := strings.TrimSpace(envelope.Message)
			if message == "" {
				message = http.StatusText(response.StatusCode)
			}
			message = strings.ReplaceAll(message, c.apiKey, "[REDACTED]")
			code := response.StatusCode
			if envelope.Code != 0 {
				code = envelope.Code
			}
			return stats, fmt.Errorf("Twelve Data API error (%d): %s", code, message)
		}
		if err := json.Unmarshal(body, destination); err != nil {
			return stats, fmt.Errorf("decode Twelve Data response: %w", err)
		}
		return stats, nil
	}
	return stats, fmt.Errorf("Twelve Data API unavailable after retries (last status %d)", lastStatus)
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}
func headerInt(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
func retryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if wait := time.Until(when); wait > 0 {
			return wait
		}
	}
	return 0
}
func backoff(attempt int, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	delay := 250 * time.Millisecond * time.Duration(1<<attempt)
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}
func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
