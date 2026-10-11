package engine

import (
	"byodb/vectorforge/store"
	"byodb/vectorforge/vector"
)

// Consistency is the result of cross-checking every stored row against the
// others and against the in-memory indexes.
type Consistency struct {
	Documents             int  `json:"documents"`
	Chunks                int  `json:"chunks"`
	Vectors               int  `json:"vectors"`
	Postings              int  `json:"postings"`
	ChunkCountMismatches  int  `json:"chunk_count_mismatches"`
	ChunksWithoutDocument int  `json:"chunks_without_document"`
	ChunksWithoutVector   int  `json:"chunks_without_vector"`
	VectorsWithoutChunk   int  `json:"vectors_without_chunk"`
	VectorsWithoutPosting int  `json:"vectors_without_posting"`
	PostingsWithoutVector int  `json:"postings_without_vector"`
	MemoryMismatches      int  `json:"memory_mismatches"`
	OK                    bool `json:"ok"`
}

// Verify checks that documents, chunks, vectors and IVF postings agree with
// each other and that the in-memory indexes hold exactly the stored vectors
// with identical values. It reads one snapshot and holds the index read
// lock, so it is a diagnostic, not something to call per request.
func (e *Engine) Verify() (Consistency, error) {
	var c Consistency
	e.mu.RLock()
	defer e.mu.RUnlock()
	err := e.st.View(func(r *store.Reader) error {
		docChunks := map[string]int{}
		if err := r.Documents(func(d store.Document) error {
			docChunks[d.ID] = d.ChunkCount
			return nil
		}); err != nil {
			return err
		}
		c.Documents = len(docChunks)
		seenChunks := map[string]int{}
		chunkVectors := map[int64]bool{}
		if err := r.AllChunks(func(ch store.Chunk) error {
			c.Chunks++
			if _, ok := docChunks[ch.DocID]; !ok {
				c.ChunksWithoutDocument++
			}
			seenChunks[ch.DocID]++
			chunkVectors[ch.VectorID] = true
			return nil
		}); err != nil {
			return err
		}
		for id, n := range docChunks {
			if seenChunks[id] != n {
				c.ChunkCountMismatches++
			}
		}
		stored := map[int64]bool{}
		if err := r.Vectors(func(v store.StoredVector) error {
			c.Vectors++
			stored[v.ID] = true
			if !chunkVectors[v.ID] {
				c.VectorsWithoutChunk++
			}
			mem, ok := e.flat.Vector(v.ID)
			if !ok || !equal(mem, v.Vector) {
				c.MemoryMismatches++
			}
			if e.ivf != nil {
				if _, ok := e.ivf.ListOf(v.ID); !ok {
					c.MemoryMismatches++
				}
			}
			return nil
		}); err != nil {
			return err
		}
		for id := range chunkVectors {
			if !stored[id] {
				c.ChunksWithoutVector++
			}
		}
		if e.flat.Len() != c.Vectors {
			c.MemoryMismatches += abs(e.flat.Len() - c.Vectors)
		}
		state, trained, err := r.IVF()
		if err != nil {
			return err
		}
		if trained {
			c.Postings = len(state.Postings) + len(state.Orphans)
			c.PostingsWithoutVector = len(state.Orphans)
			for id, list := range state.Postings {
				if !stored[id] {
					c.PostingsWithoutVector++
				} else if e.ivf != nil {
					if memList, ok := e.ivf.ListOf(id); !ok || memList != list {
						c.MemoryMismatches++
					}
				}
			}
			for id := range stored {
				if _, ok := state.Postings[id]; !ok {
					c.VectorsWithoutPosting++
				}
			}
		} else if e.ivf != nil {
			c.MemoryMismatches++
		}
		return nil
	})
	c.OK = err == nil && c.ChunkCountMismatches == 0 && c.ChunksWithoutDocument == 0 && c.ChunksWithoutVector == 0 &&
		c.VectorsWithoutChunk == 0 && c.VectorsWithoutPosting == 0 && c.PostingsWithoutVector == 0 && c.MemoryMismatches == 0
	return c, err
}

func equal(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return vector.Validate(a, len(b)) == nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
