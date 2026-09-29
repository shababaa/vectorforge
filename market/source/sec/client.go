// Package sec retrieves a bounded recent-filing metadata page from EDGAR.
// It deliberately does not scrape filing bodies or infer financial facts.
package sec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"byodb/market"
)

type Config struct {
	UserAgent  string
	HTTPClient *http.Client
}
type Client struct {
	agent string
	http  *http.Client
	mu    sync.Mutex
	next  time.Time
}

func New(config Config) (*Client, error) {
	if len(config.UserAgent) < 8 || len(config.UserAgent) > 200 || !strings.Contains(config.UserAgent, "@") || strings.ContainsAny(config.UserAgent, "\r\n") {
		return nil, errors.New("SEC requires an identifying User-Agent with your contact email")
	}
	hc := http.Client{Timeout: 20 * time.Second}
	if config.HTTPClient != nil {
		hc = *config.HTTPClient
	}
	if hc.Timeout <= 0 || hc.Timeout > 20*time.Second {
		hc.Timeout = 20 * time.Second
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("SEC redirects disabled") }
	return &Client{agent: config.UserAgent, http: &hc}, nil
}

// Reserve at most five requests/second per client, shared by concurrent calls.
func (c *Client) wait(ctx context.Context, delay time.Duration) error {
	c.mu.Lock()
	at := time.Now().Add(delay)
	if c.next.After(at) {
		at = c.next
	}
	c.next = at.Add(200 * time.Millisecond)
	c.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) RecentFilings(ctx context.Context, symbol, cik string) ([]market.FilingSource, error) {
	if !regexp.MustCompile(`^[0-9]{10}$`).MatchString(cik) {
		return nil, errors.New("CIK must contain exactly ten digits")
	}
	endpoint := "https://data.sec.gov/submissions/CIK" + cik + ".json"
	delay := time.Duration(0)
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.wait(ctx, delay); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.agent)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("SEC transport failed")
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			delay = time.Duration(1<<attempt) * time.Second
			if value := resp.Header.Get("Retry-After"); value != "" {
				if n, e := strconv.Atoi(value); e == nil && n >= 0 {
					if n > 30 {
						delay = 31 * time.Second
					} else {
						delay = max(delay, time.Duration(n)*time.Second)
					}
				} else if at, e := http.ParseTime(value); e == nil {
					delay = max(delay, time.Until(at))
				}
			}
			resp.Body.Close()
			if delay > 30*time.Second {
				return nil, errors.New("SEC requests a longer cooldown; retry this job later")
			}
			if attempt == 2 {
				return nil, fmt.Errorf("SEC retry budget exhausted (HTTP %d)", resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("SEC HTTP status %d", resp.StatusCode)
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, errors.New("SEC response read failed")
		}
		if len(b) > 4<<20 {
			return nil, errors.New("SEC response exceeds 4 MiB")
		}
		return ParseSubmissions(b, symbol, cik)
	}
	return nil, errors.New("SEC request failed")
}

func ParseSubmissions(b []byte, symbol, cik string) ([]market.FilingSource, error) {
	var payload struct {
		CIK     flexibleCIK `json:"cik"`
		Tickers []string    `json:"tickers"`
		Filings struct {
			Recent struct {
				Accession []string `json:"accessionNumber"`
				Form      []string `json:"form"`
				Accepted  []string `json:"acceptanceDateTime"`
				Document  []string `json:"primaryDocument"`
			} `json:"recent"`
		} `json:"filings"`
	}
	if len(b) > 4<<20 || json.Unmarshal(b, &payload) != nil {
		return nil, errors.New("invalid SEC submissions JSON")
	}
	if !regexp.MustCompile(`^[0-9]{10}$`).MatchString(cik) || strings.TrimLeft(cik, "0") != strings.TrimLeft(string(payload.CIK), "0") {
		return nil, errors.New("SEC CIK mismatch")
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	found := false
	for _, ticker := range payload.Tickers {
		if strings.EqualFold(ticker, symbol) {
			found = true
		}
	}
	if !found {
		return nil, errors.New("requested symbol does not match SEC company tickers")
	}
	p := payload.Filings.Recent
	if len(p.Accession) > 10000 || len(p.Accession) != len(p.Form) || len(p.Accession) != len(p.Accepted) || len(p.Accession) != len(p.Document) {
		return nil, errors.New("inconsistent SEC submissions arrays")
	}
	out := []market.FilingSource{}
	for i, form := range p.Form {
		switch form {
		case "10-K", "10-Q", "8-K", "10-K/A", "10-Q/A", "8-K/A":
		default:
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, p.Accepted[i])
		if err != nil {
			return nil, errors.New("invalid SEC acceptance timestamp")
		}
		v, err := market.NormalizeFilingSource(market.FilingSource{Symbol: symbol, CIK: cik, Form: form, Accession: p.Accession[i], PrimaryDocument: p.Document[i], PublishedAt: at.Unix()})
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PublishedAt == out[j].PublishedAt {
			return out[i].Accession > out[j].Accession
		}
		return out[i].PublishedAt > out[j].PublishedAt
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out, nil
}

// flexibleCIK accepts the numeric CIK used by older fixtures and the
// zero-padded string the live submissions endpoint currently returns.
type flexibleCIK string

func (c *flexibleCIK) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		return errors.New("missing CIK")
	}
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*c = flexibleCIK(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	*c = flexibleCIK(number.String())
	return nil
}
