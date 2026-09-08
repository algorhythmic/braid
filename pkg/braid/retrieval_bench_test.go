package braid

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// BRAID_BENCH_BATCH may point to an adapter-generated Batch JSON file. Its
// contents stay outside the repository. Without it, use synthetic records.
// This benchmark intentionally uses the pre-v2 API so the same file can measure
// a baseline checkout. It excludes ingestion, providers, and process startup.
func BenchmarkRetrieval(b *testing.B) {
	seed := Batch{}
	if path := os.Getenv("BRAID_BENCH_BATCH"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		if err = json.Unmarshal(raw, &seed); err != nil {
			b.Fatal(err)
		}
		if len(seed.Nodes) == 0 {
			b.Fatal("empty benchmark batch")
		}
	}
	for _, count := range []int{1998, 19980} {
		for _, lambda := range []float64{1, .4} {
			b.Run(fmt.Sprintf("rows=%d/lambda=%.1f", count, lambda), func(b *testing.B) {
				c := DefaultConfig()
				c.Store.Path = filepath.Join(b.TempDir(), "bench.db")
				c.Defaults.Weights = weights("lexical")
				c.Defaults.MMRLambda = lambda
				c.Defaults.Budget.Max = 20
				c.Embed.Model = "benchmark-v1"
				e, err := Open(testCtx, c)
				if err != nil {
					b.Fatal(err)
				}
				defer e.Close()
				batch := Batch{}
				for i := 0; i < count; i++ {
					n := node(fmt.Sprintf("n%05d", i), "binding", fmt.Sprintf("find files capability%d %s", i%20, strings.Repeat("metadata ", 10)), 1)
					if len(seed.Nodes) > 0 {
						n = seed.Nodes[i%len(seed.Nodes)]
						n.ID = fmt.Sprintf("n%05d", i)
						cost := 1.
						n.Cost = &cost
					}
					batch.Nodes = append(batch.Nodes, n)
					batch.Embeddings = append(batch.Embeddings, Embedding{NodeID: n.ID, Model: c.Embed.Model, Vec: []float32{float32(i%20 + 1), 1}})
					batch.Edges = append(batch.Edges, Edge{Src: n.ID, Dst: fmt.Sprintf("n%05d", (i+1)%count), Type: "related", Weight: 1, TS: testTime})
				}
				if err = e.Upsert(testCtx, batch); err != nil {
					b.Fatal(err)
				}
				q := Query{Text: "find files", At: testTime}
				for i := 0; i < 3; i++ {
					if _, err = e.Query(testCtx, q); err != nil {
						b.Fatal(err)
					}
				}
				samples := make([]int64, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					start := time.Now()
					if _, err = e.Query(testCtx, q); err != nil {
						b.Fatal(err)
					}
					samples[i] = time.Since(start).Nanoseconds()
				}
				b.StopTimer()
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				b.ReportMetric(float64(samples[(len(samples)-1)*50/100])/1000, "p50-us")
				b.ReportMetric(float64(samples[(len(samples)-1)*95/100])/1000, "p95-us")
			})
		}
	}
}
