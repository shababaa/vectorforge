package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultBatch     = 64
	maxResponseBytes = 64 << 20
	attempts         = 3
)

// Client calls the VectorForge embedding service.
type Client struct {
	base string
	http *http.Client

	mu   sync.Mutex
	info *Info
}

// NewClient returns a client for an http(s) base URL such as
// http://127.0.0.1:8090.
func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("embedder URL must be http(s)://host[:port], got %q", baseURL)
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}}, nil
}

type healthResponse struct {
	Status       string `json:"status"`
	ModelID      string `json:"model_id"`
	Dimension    int    `json:"dimension"`
	MaxSeqLength int    `json:"max_seq_length"`
	Normalized   bool   `json:"normalized"`
	MaxBatch     int    `json:"max_batch"`
	MaxTextChars int    `json:"max_text_chars"`
}

// Info returns the service's model identity, fetching /health once.
func (c *Client) Info(ctx context.Context) (Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info != nil {
		return *c.info, nil
	}
	var h healthResponse
	if err := c.do(ctx, http.MethodGet, "/health", nil, &h); err != nil {
		return Info{}, err
	}
	if h.Status != "ok" || h.ModelID == "" || h.Dimension < 1 || !h.Normalized {
		return Info{}, fmt.Errorf("%w: unhealthy or unnormalized service: %+v", ErrBadResponse, h)
	}
	info := Info{ModelID: h.ModelID, Dimension: h.Dimension, MaxSeqLength: h.MaxSeqLength, MaxBatch: h.MaxBatch, MaxTextChars: h.MaxTextChars}
	c.info = &info
	return info, nil
}

// Ping performs one uncached /health request without retries.
func (c *Client) Ping(ctx context.Context) error {
	var h healthResponse
	return c.doAttempts(ctx, http.MethodGet, "/health", nil, &h, 1)
}

// WaitReady polls /health until the service answers or ctx ends. The Python
// service takes several seconds to load its model.
func (c *Client) WaitReady(ctx context.Context) (Info, error) {
	for {
		info, err := c.Info(ctx)
		if err == nil || ctx.Err() != nil {
			return info, err
		}
		select {
		case <-ctx.Done():
			return Info{}, fmt.Errorf("embedding service not ready: %w (last error: %v)", ctx.Err(), err)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type batchRequest struct {
	Texts []string `json:"texts"`
	Kind  Kind     `json:"kind"`
}

type batchResponse struct {
	ModelID    string      `json:"model_id"`
	Dimension  int         `json:"dimension"`
	Embeddings [][]float32 `json:"embeddings"`
	Tokens     []int       `json:"tokens"`
	Truncated  []bool      `json:"truncated"`
}

// Embed embeds texts in service-sized batches. It fails if the service's
// model changes between calls, so one collection never mixes vector spaces.
func (c *Client) Embed(ctx context.Context, texts []string, kind Kind) (Result, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return Result{}, err
	}
	batch := defaultBatch
	if info.MaxBatch > 0 && info.MaxBatch < batch {
		batch = info.MaxBatch
	}
	out := Result{Vectors: make([][]float32, 0, len(texts)), Tokens: make([]int, 0, len(texts)), Truncated: make([]bool, 0, len(texts))}
	for start := 0; start < len(texts); start += batch {
		part := texts[start:min(start+batch, len(texts))]
		var resp batchResponse
		if err := c.do(ctx, http.MethodPost, "/embed/batch", batchRequest{Texts: part, Kind: kind}, &resp); err != nil {
			return Result{}, err
		}
		if resp.ModelID != info.ModelID || resp.Dimension != info.Dimension {
			return Result{}, fmt.Errorf("%w: expected %s (%d-d), service returned %s (%d-d)",
				ErrModelChanged, info.ModelID, info.Dimension, resp.ModelID, resp.Dimension)
		}
		if len(resp.Embeddings) != len(part) || len(resp.Tokens) != len(part) || len(resp.Truncated) != len(part) {
			return Result{}, fmt.Errorf("%w: %d texts produced %d embeddings", ErrBadResponse, len(part), len(resp.Embeddings))
		}
		if err := normalizeAll(resp.Embeddings, info.Dimension); err != nil {
			return Result{}, err
		}
		out.Vectors = append(out.Vectors, resp.Embeddings...)
		out.Tokens = append(out.Tokens, resp.Tokens...)
		out.Truncated = append(out.Truncated, resp.Truncated...)
	}
	return out, nil
}

// StatusError is a non-2xx response from the service.
type StatusError struct {
	Code   int
	Detail string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("embedding service returned HTTP %d: %s", e.Code, e.Detail)
}

// do performs one request, retrying connection failures and 5xx responses.
// Embedding is a pure function of the input, so retries are safe.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doAttempts(ctx, method, path, body, out, attempts)
}

func (c *Client) doAttempts(ctx context.Context, method, path string, body, out any, attempts int) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 250 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = fmt.Errorf("embedding service unreachable: %w", err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode >= 300 {
			last = &StatusError{Code: resp.StatusCode, Detail: strings.TrimSpace(string(data[:min(len(data), 500)]))}
			if resp.StatusCode >= 500 {
				continue
			}
			return last
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%w: %v", ErrBadResponse, err)
		}
		return nil
	}
	return last
}

// IsUnavailable reports whether err means the service could not be reached
// or failed internally, as opposed to rejecting the input.
func IsUnavailable(err error) bool {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Code >= 500
	}
	return err != nil && !errors.Is(err, ErrBadResponse) && !errors.Is(err, ErrModelChanged) &&
		!errors.Is(err, context.Canceled)
}
