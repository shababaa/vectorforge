package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"byodb/vectorforge/index"
	"byodb/vectorforge/vector"
)

type exportMeta struct {
	Dataset   string   `json:"dataset"`
	Model     string   `json:"model"`
	Dimension int      `json:"dimension"`
	DocIDs    []string `json:"doc_ids"`
	QueryIDs  []string `json:"query_ids"`
	Split     string   `json:"split"`
}

func readF32(path string, dim int) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw)%(4*dim) != 0 {
		return nil, fmt.Errorf("%s: %d bytes is not a multiple of %d-d float32 rows", path, len(raw), dim)
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	for r := 0; r < len(out)/dim; r++ {
		if v := out[r*dim : (r+1)*dim]; !vector.IsUnit(v) {
			return nil, fmt.Errorf("%s: row %d is not unit length", path, r)
		}
	}
	return out, nil
}

// loadExport reads corpus.f32, queries.f32 and meta.json written by
// vectorforge/ml (vfml.scifact or vfml.export).
func loadExport(dir string, maxQueries int) (*Dataset, error) {
	var meta exportMeta
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	data, err := readF32(filepath.Join(dir, "corpus.f32"), meta.Dimension)
	if err != nil {
		return nil, err
	}
	queries, err := readF32(filepath.Join(dir, "queries.f32"), meta.Dimension)
	if err != nil {
		return nil, err
	}
	if len(data)/meta.Dimension != len(meta.DocIDs) || len(queries)/meta.Dimension != len(meta.QueryIDs) {
		return nil, fmt.Errorf("%s: matrix rows do not match meta.json ids", dir)
	}
	nq := len(meta.QueryIDs)
	if maxQueries > 0 && nq > maxQueries {
		nq = maxQueries
	}
	d := &Dataset{Name: meta.Dataset + "-" + meta.Model, Kind: "real", N: len(meta.DocIDs), Dim: meta.Dimension, Queries: nq,
		Source: filepath.ToSlash(dir), Corpus: meta.Dataset, Model: meta.Model, split: meta.Split,
		data: data, queries: queries[:nq*meta.Dimension], docIDs: meta.DocIDs, queryIDs: meta.QueryIDs[:nq]}
	d.ids = make([]int64, d.N)
	for i := range d.ids {
		d.ids[i] = int64(i)
	}
	return d, nil
}

// RunFileInfo points at a ranked-list file for relevance evaluation.
type RunFileInfo struct {
	Strategy string `json:"strategy"`
	NList    int    `json:"nlist,omitempty"`
	NProbe   int    `json:"nprobe,omitempty"`
	Path     string `json:"path"`
}

type runFile struct {
	Dataset  string              `json:"dataset"`
	Corpus   string              `json:"corpus"`
	Model    string              `json:"model"`
	Split    string              `json:"split"`
	Strategy string              `json:"strategy"`
	NList    int                 `json:"nlist,omitempty"`
	NProbe   int                 `json:"nprobe,omitempty"`
	K        int                 `json:"k"`
	Runs     map[string][]string `json:"runs"`
}

// writeRuns stores top-k document IDs per query so that Python can score
// ANN results against human relevance labels, not just against exact search.
func writeRuns(d *Dataset, dir, strategy string, nlist, nprobe, k int, search func(q []float32) []vector.Hit) (RunFileInfo, error) {
	rf := runFile{Dataset: d.Name, Corpus: d.Corpus, Model: d.Model, Split: d.split, Strategy: strategy,
		NList: nlist, NProbe: nprobe, K: k, Runs: map[string][]string{}}
	for i := 0; i < d.Queries; i++ {
		hits := search(d.query(i))
		ids := make([]string, len(hits))
		for j, h := range hits {
			ids[j] = d.docIDs[h.ID]
		}
		rf.Runs[d.queryIDs[i]] = ids
	}
	name := fmt.Sprintf("%s-%s", d.Name, strategy)
	if strategy == "ivf" {
		name += fmt.Sprintf("-nlist%d-nprobe%d", nlist, nprobe)
	}
	path := filepath.Join(dir, name+".json")
	raw, err := json.Marshal(rf)
	if err != nil {
		return RunFileInfo{}, err
	}
	return RunFileInfo{Strategy: strategy, NList: nlist, NProbe: nprobe, Path: filepath.ToSlash(path)}, os.WriteFile(path, raw, 0o644)
}

func realRuns(d *Dataset, flat *index.Flat, ivf *index.IVF, nlist int, nprobes []int, dir string) ([]RunFileInfo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	info, err := writeRuns(d, dir, "exact", 0, 0, 100, func(q []float32) []vector.Hit { h, _ := flat.Search(q, 100, nil); return h })
	if err != nil {
		return nil, err
	}
	out := []RunFileInfo{info}
	for _, np := range nprobes {
		if np > nlist {
			continue
		}
		info, err := writeRuns(d, dir, "ivf", nlist, np, 100, func(q []float32) []vector.Hit { h, _ := ivf.Search(q, 100, np, nil); return h })
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}
