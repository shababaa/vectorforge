package market

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"byodb"
)

// FilingSource is a normalized SEC submissions row, not extracted filing text.
// PublishedAt is SEC acceptance time; RetrievedAt is our first observation.
type FilingSource struct {
	SourceID        string `json:"source_id"`
	Symbol          string `json:"symbol"`
	CIK             string `json:"cik"`
	Accession       string `json:"accession"`
	Form            string `json:"form"`
	PrimaryDocument string `json:"primary_document"`
	URL             string `json:"url"`
	PublishedAt     int64  `json:"published_at"`
	RetrievedAt     int64  `json:"retrieved_at"`
	Digest          string `json:"digest"`
}

var cikPattern = regexp.MustCompile(`^[0-9]{10}$`)
var accessionPattern = regexp.MustCompile(`^[0-9]{10}-[0-9]{2}-[0-9]{6}$`)
var documentPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,159}\.htm[l]?$`)

func NormalizeFilingSource(in FilingSource) (FilingSource, error) {
	var err error
	if in.Symbol, err = normalizeSymbol(in.Symbol); err != nil {
		return in, err
	}
	if !cikPattern.MatchString(in.CIK) || strings.TrimLeft(in.CIK, "0") == "" || !accessionPattern.MatchString(in.Accession) || !documentPattern.MatchString(in.PrimaryDocument) {
		return in, errors.New("invalid SEC filing identifiers")
	}
	switch in.Form {
	case "10-K", "10-Q", "8-K", "10-K/A", "10-Q/A", "8-K/A":
	default:
		return in, errors.New("unsupported SEC form")
	}
	if in.PublishedAt <= 0 {
		return in, errors.New("filing acceptance time is required")
	}
	expectedURL := "https://www.sec.gov/Archives/edgar/data/" + strings.TrimLeft(in.CIK, "0") + "/" + strings.ReplaceAll(in.Accession, "-", "") + "/" + in.PrimaryDocument
	if in.URL != "" && in.URL != expectedURL {
		return in, errors.New("filing URL does not match SEC identifiers")
	}
	if _, err := url.ParseRequestURI(expectedURL); err != nil {
		return in, err
	}
	in.URL = expectedURL
	in.SourceID, in.Digest, in.RetrievedAt = "", "", 0
	b, err := json.Marshal(in)
	if err != nil {
		return in, err
	}
	in.Digest = digestBytes(b)
	in.SourceID = "sec_" + in.Digest // Content-addressed revisions retain their own lineage.
	return in, nil
}

// StoreFilingSources ignores caller-supplied first-observation times and keeps
// the original time on retries. The complete batch is validated before writes.
func (r *Repository) StoreFilingSources(input []FilingSource) ([]FilingSource, error) {
	if len(input) > 100 {
		return nil, errors.New("filing batch exceeds 100 rows")
	}
	out := make([]FilingSource, len(input))
	now := r.now().UTC().Unix()
	for i, item := range input {
		v, err := NormalizeFilingSource(item)
		if err != nil {
			return nil, fmt.Errorf("filing %d: %w", i, err)
		}
		if v.PublishedAt > now {
			return nil, errors.New("filing acceptance time is in the future")
		}
		v.RetrievedAt = now
		out[i] = v
	}
	err := r.writeTransaction(func(tx *byodb.DBTX) error {
		for i := range out {
			key := (&byodb.Record{}).AddString("source_id", out[i].SourceID)
			exists, err := tx.Get(tableSources, key)
			if err != nil {
				return err
			}
			if exists {
				out[i] = filingFromRecord(*key)
				continue
			}
			if _, err := tx.Set(tableSources, filingRecord(out[i]), byodb.MODE_INSERT_ONLY); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func filingRecord(v FilingSource) byodb.Record {
	return *(&byodb.Record{}).AddString("source_id", v.SourceID).AddString("symbol", v.Symbol).AddString("cik", v.CIK).AddString("accession", v.Accession).AddString("form", v.Form).AddString("primary_document", v.PrimaryDocument).AddString("url", v.URL).AddInt64("published_at", v.PublishedAt).AddInt64("retrieved_at", v.RetrievedAt).AddString("digest", v.Digest)
}

func filingFromRecord(v byodb.Record) FilingSource {
	return FilingSource{SourceID: v.Get("source_id").String(), Symbol: v.Get("symbol").String(), CIK: v.Get("cik").String(), Accession: v.Get("accession").String(), Form: v.Get("form").String(), PrimaryDocument: v.Get("primary_document").String(), URL: v.Get("url").String(), PublishedAt: v.Get("published_at").I64, RetrievedAt: v.Get("retrieved_at").I64, Digest: v.Get("digest").String()}
}

func filingsAt(tx *byodb.DBTX, symbol string, asOf int64) ([]FilingSource, error) {
	low := *(&byodb.Record{}).AddString("symbol", symbol).AddInt64("published_at", max(int64(1), asOf-365*86400))
	high := *(&byodb.Record{}).AddString("symbol", symbol).AddInt64("published_at", asOf)
	sc := &byodb.Scanner{Cmp1: byodb.CMP_LE, Key1: high, Cmp2: byodb.CMP_GE, Key2: low, Index: []string{"symbol", "published_at"}}
	if err := tx.Scan(tableSources, sc); err != nil {
		return nil, err
	}
	out := []FilingSource{}
	seen := map[string]bool{}
	for scanned := 0; sc.Valid() && len(out) < 3; scanned++ {
		if scanned >= 1000 {
			return nil, errors.New("filing evidence scan exceeds 1000 rows")
		}
		var row byodb.Record
		if err := sc.Deref(&row); err != nil {
			return nil, err
		}
		v := filingFromRecord(row)
		canonical, err := NormalizeFilingSource(v)
		if err != nil || canonical.Digest != v.Digest || canonical.SourceID != v.SourceID || v.RetrievedAt < v.PublishedAt {
			return nil, errors.New("filing source integrity check failed")
		}
		if v.RetrievedAt <= asOf && !seen[v.Accession] {
			out = append(out, v)
			seen[v.Accession] = true
		}
		sc.Next()
	}
	return out, nil
}
