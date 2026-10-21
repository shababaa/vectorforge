package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"byodb/vectorforge/vector"
)

func TestSummarizeNearestRank(t *testing.T) {
	var samples []time.Duration
	for i := 100; i >= 1; i-- { // unsorted input
		samples = append(samples, time.Duration(i)*time.Microsecond)
	}
	l := summarize(samples)
	if l.N != 100 || l.P50 != 50 || l.P95 != 95 || l.P99 != 99 || l.Max != 100 || l.Mean != 50.5 {
		t.Fatalf("summary %+v", l)
	}
	if one := summarize([]time.Duration{7 * time.Microsecond}); one.P50 != 7 || one.P99 != 7 {
		t.Fatalf("single sample %+v", one)
	}
	if summarize(nil).N != 0 {
		t.Fatal("empty input")
	}
}

func hits(ids ...int64) []vector.Hit {
	out := make([]vector.Hit, len(ids))
	for i, id := range ids {
		out[i] = vector.Hit{ID: id, Score: float32(len(ids) - i)}
	}
	return out
}

func TestMeanRecallAndOperatingPoint(t *testing.T) {
	exact := [][]vector.Hit{hits(1, 2, 3, 4), hits(5, 6, 7, 8)}
	approx := [][]vector.Hit{hits(1, 2, 9, 4), hits(5, 6, 7, 8)}
	if r := meanRecall(exact, approx, 4); r != 0.875 {
		t.Fatalf("recall %v", r)
	}
	if r := meanRecall(exact, approx, 2); r != 1 {
		t.Fatalf("recall@2 %v", r)
	}
	rows := []IVFRow{{NList: 10, NProbe: 1, RecallAt10: 0.5}, {NList: 10, NProbe: 2, RecallAt10: 0.96},
		{NList: 20, NProbe: 1, RecallAt10: 0.99}, {NList: 10, NProbe: 4, RecallAt10: 0.99}}
	if np := nprobeForRecall(rows, 10, 0.95); np != 2 {
		t.Fatalf("nprobe %d", np)
	}
	if np := nprobeForRecall(rows, 10, 0.999); np != 4 {
		t.Fatalf("unreachable target should return the largest nprobe, got %d", np)
	}
}

func TestSweepOnSmallData(t *testing.T) {
	d := syntheticDataset("clustered", 2000, 32, 60, 3)
	rep, flat, ivfs, truth := runSweep(d, sweep{nlists: []int{20}, nprobes: []int{1, 20, 40}, k: 10, deep: true, seed: 1, keep: 20}, t.Logf)
	if flat.Len() != 2000 || len(truth) != 60 || ivfs[20] == nil {
		t.Fatalf("flat %d truth %d", flat.Len(), len(truth))
	}
	if len(rep.IVF) != 2 { // nprobe 40 > nlist is skipped
		t.Fatalf("rows %+v", rep.IVF)
	}
	full := rep.IVF[1]
	if full.NProbe != 20 || full.RecallAt10 != 1 || full.RecallAt100 != 1 || full.FractionScored != 1 {
		t.Fatalf("probing every list must match exact search: %+v", full)
	}
	if low := rep.IVF[0]; low.RecallAt10 > full.RecallAt10 || low.FractionScored >= 1 {
		t.Fatalf("nprobe=1 row %+v", low)
	}
	if rep.Exact.Latency.N != 60 || rep.Builds[0].Balance.TotalItems != 2000 {
		t.Fatalf("exact %+v builds %+v", rep.Exact, rep.Builds)
	}
}

func TestLoadExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, rows [][]float32) {
		var raw []byte
		for _, r := range rows {
			for _, v := range r {
				raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(v))
			}
		}
		os.WriteFile(filepath.Join(dir, name), raw, 0o644)
	}
	write("corpus.f32", [][]float32{{1, 0}, {0, 1}, {0.6, 0.8}})
	write("queries.f32", [][]float32{{0.8, 0.6}})
	meta, _ := json.Marshal(exportMeta{Dataset: "tiny", Model: "m", Dimension: 2, DocIDs: []string{"a", "b", "c"}, QueryIDs: []string{"q1"}})
	os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644)
	d, err := loadExport(dir, 0)
	if err != nil || d.N != 3 || d.Queries != 1 || d.Name != "tiny-m" {
		t.Fatalf("load %+v %v", d, err)
	}
	flat, _ := buildFlat(d)
	ivf, _ := buildIVF(d, 1, 1)
	runs, err := realRuns(d, flat, ivf, 1, []int{1, 2}, filepath.Join(dir, "runs"))
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs %+v %v", runs, err)
	}
	var rf runFile
	raw, _ := os.ReadFile(filepath.FromSlash(runs[0].Path))
	json.Unmarshal(raw, &rf)
	if got := rf.Runs["q1"]; len(got) != 3 || got[0] != "c" || got[2] != "b" {
		t.Fatalf("ranked ids %v", got)
	}
	if rf.Corpus != "tiny" || rf.Model != "m" || rf.Strategy != "exact" {
		t.Fatalf("run file header %+v", rf)
	}

	write("queries.f32", [][]float32{{2, 0}})
	if _, err := loadExport(dir, 0); err == nil {
		t.Fatal("non-unit query rows must be rejected")
	}
	write("queries.f32", [][]float32{{1, 0}, {0, 1}})
	if _, err := loadExport(dir, 0); err == nil {
		t.Fatal("row count mismatch with meta.json must be rejected")
	}
}
