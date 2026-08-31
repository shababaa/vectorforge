// Package ollama implements the native local /api/chat structured-output API.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"byodb/market"
)

type Config struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

type Client struct {
	endpoint, model string
	http            *http.Client
}

func New(config Config) (*Client, error) {
	if config.BaseURL == "" {
		config.BaseURL = "http://127.0.0.1:11434"
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Ollama requires a loopback HTTP origin without credentials or paths")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Ollama endpoint must use a numeric loopback address")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]{0,99}$`).MatchString(config.Model) {
		return nil, errors.New("explicit valid Ollama model name is required")
	}
	hc := http.Client{Timeout: 60 * time.Second}
	if config.HTTPClient != nil {
		hc = *config.HTTPClient
	}
	if hc.Timeout <= 0 || hc.Timeout > 60*time.Second {
		hc.Timeout = 60 * time.Second
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }
	u.Path = "/api/chat"
	return &Client{u.String(), config.Model, &hc}, nil
}

// Complete performs one bounded request. AnalysisService owns the single retry
// budget, so transport retries and validation retries cannot multiply.
func (c *Client) Complete(ctx context.Context, request market.LLMRequest) (market.LLMResponse, error) {
	var empty market.LLMResponse
	if len(request.InputJSON) > 64<<10 || len(request.SystemPrompt) > 8192 || !json.Valid(request.InputJSON) {
		return empty, errors.New("invalid or oversized analyst request")
	}
	body, err := json.Marshal(map[string]any{"model": c.model, "messages": []map[string]string{{"role": "system", "content": request.SystemPrompt}, {"role": "user", "content": string(request.InputJSON)}}, "stream": false, "format": market.LLMOutputSchema(), "options": map[string]any{"temperature": 0, "seed": 7, "num_predict": 512, "num_ctx": 8192}})
	if err != nil {
		return empty, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return empty, errors.New("cannot build Ollama request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, &market.RetryableLLMError{Code: "ollama_transport"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return empty, &market.RetryableLLMError{Code: fmt.Sprintf("ollama_http_%d", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("Ollama HTTP status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil {
		return empty, &market.RetryableLLMError{Code: "ollama_read"}
	}
	if len(b) > 64<<10 {
		return empty, market.ErrInvalidLLMOutput
	}
	var envelope struct {
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
		Error      string `json:"error"`
		Message    struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &envelope) != nil || !envelope.Done || envelope.DoneReason == "length" || envelope.Error != "" || envelope.Message.Role != "assistant" || len(envelope.Message.ToolCalls) > 0 {
		return empty, market.ErrInvalidLLMOutput
	}
	return market.DecodeLLMResponse([]byte(envelope.Message.Content))
}
