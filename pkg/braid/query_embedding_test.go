package braid

import (
	"context"
	"errors"
	"math"
	"testing"
)

// Package A: https://github.com/algorhythmic/muninn/blob/8bdda8b/docs/braid-integration/A-EMBEDDING-COMPATIBILITY.md
// All fixtures are synthetic; these tests assert dispatch and compatibility,
// not retrieval quality.
type queryEmbeddingProvider struct{ calls int }

func (*queryEmbeddingProvider) Model() string { return "synthetic-space" }
func (p *queryEmbeddingProvider) Embed(_ context.Context, _ []string) ([][]float32, error) {
	p.calls++
	return [][]float32{{0, 1}}, nil
}

func queryEmbeddingEngine(t *testing.T, opts ...Option) *Engine {
	t.Helper()
	c := DefaultConfig()
	c.Embed.Model = "synthetic-space"
	c.Defaults.Weights = weights("dense")
	c.Defaults.MMRLambda = 1
	e := openTest(t, c, opts...)
	mustUpsert(t, e, Batch{
		Nodes: []Node{node("a", "fixture", "alpha", 1), node("b", "fixture", "beta", 1), node("c", "fixture", "centroid", 1)},
		Embeddings: []Embedding{
			{NodeID: "a", Model: c.Embed.Model, Vec: []float32{1, 0}},
			{NodeID: "b", Model: c.Embed.Model, Vec: []float32{0, 1}},
			{NodeID: "c", Model: c.Embed.Model, Vec: []float32{1, 1}},
		},
	})
	return e
}

func requireEmbeddingError(t *testing.T, err error, code string) {
	t.Helper()
	var incompatibility *EmbeddingIncompatibilityError
	if !errors.As(err, &incompatibility) {
		t.Fatalf("got %T (%v), want *EmbeddingIncompatibilityError", err, err)
	}
	if incompatibility.Code != code || incompatibility.Message == "" {
		t.Fatalf("got %+v, want code %q and a message", incompatibility, code)
	}
}

func TestQueryEmbeddingValidation(t *testing.T) {
	p := &queryEmbeddingProvider{}
	e := queryEmbeddingEngine(t, WithProvider(p))
	cases := []struct {
		name, model, code string
		vector            []float32
		anchors           []string
		text              string
	}{
		{"nil-vector", "synthetic-space", "embedding_empty_vector", nil, nil, ""},
		{"empty-vector", "synthetic-space", "embedding_empty_vector", []float32{}, nil, ""},
		{"zero-vector", "synthetic-space", "embedding_zero_vector", []float32{0, 0}, nil, ""},
		{"nan-vector", "synthetic-space", "embedding_nonfinite_vector", []float32{1, float32(math.NaN())}, nil, ""},
		{"positive-infinity", "synthetic-space", "embedding_nonfinite_vector", []float32{float32(math.Inf(1)), 0}, nil, ""},
		{"negative-infinity", "synthetic-space", "embedding_nonfinite_vector", []float32{0, float32(math.Inf(-1))}, nil, ""},
		{"wrong-model", "other-space", "embedding_model_mismatch", []float32{1, 0}, nil, ""},
		{"empty-model", "", "embedding_model_mismatch", []float32{1, 0}, nil, ""},
		{"model-is-exact", "synthetic-space ", "embedding_model_mismatch", []float32{1, 0}, nil, ""},
		{"wrong-dimension", "synthetic-space", "embedding_dimension_mismatch", []float32{1, 0, 0}, nil, ""},
		{"ambiguous-anchors", "synthetic-space", "embedding_ambiguous_anchors", []float32{1, 0}, []string{"b"}, ""},
		{"whitespace-anchors", "synthetic-space", "embedding_ambiguous_anchors", []float32{1, 0}, []string{"b"}, " \t\n"},
	}
	for _, tc := range cases {
		for _, path := range []string{"fused", "dense-disabled", "raw-dense", "raw-zero-limit"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				q := Query{Text: tc.text, Anchors: tc.anchors, QueryEmbedding: &QueryEmbedding{Model: tc.model, Vector: tc.vector}}
				var err error
				if path == "raw-dense" || path == "raw-zero-limit" {
					r, _ := e.Retriever("dense")
					limit := 10
					if path == "raw-zero-limit" {
						limit = 0
					}
					_, err = r.Retrieve(testCtx, q, limit)
				} else {
					if path == "dense-disabled" {
						q.Weights = weights("temporal")
					}
					_, err = e.Query(testCtx, q)
				}
				requireEmbeddingError(t, err, tc.code)
			})
		}
	}
	if p.calls != 0 {
		t.Fatalf("validation invoked provider %d times", p.calls)
	}
}

func TestQueryEmbeddingDimensionCheckedBeforeEmptyFilters(t *testing.T) {
	e := queryEmbeddingEngine(t)
	for _, path := range []string{"fused", "dense-disabled", "raw-dense"} {
		t.Run(path, func(t *testing.T) {
			q := Query{QueryEmbedding: &QueryEmbedding{Model: "synthetic-space", Vector: []float32{1, 0, 0}}, Filters: Filters{AllowedIDs: []string{}}}
			var err error
			if path == "raw-dense" {
				r, _ := e.Retriever("dense")
				_, err = r.Retrieve(testCtx, q, 10)
			} else {
				if path == "dense-disabled" {
					q.Weights = weights("temporal")
				}
				_, err = e.Query(testCtx, q)
			}
			requireEmbeddingError(t, err, "embedding_dimension_mismatch")
		})
	}
}

func TestQueryEmbeddingDimensionUnavailable(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "synthetic-space"
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "fixture", "alpha", 1)}})
	for _, path := range []string{"fused", "dense-disabled", "raw-dense"} {
		t.Run(path, func(t *testing.T) {
			q := Query{QueryEmbedding: &QueryEmbedding{Model: c.Embed.Model, Vector: []float32{1, 0}}}
			var err error
			if path == "raw-dense" {
				r, _ := e.Retriever("dense")
				_, err = r.Retrieve(testCtx, q, 10)
			} else {
				if path == "dense-disabled" {
					q.Weights = weights("temporal")
				}
				_, err = e.Query(testCtx, q)
			}
			requireEmbeddingError(t, err, "embedding_dimension_unavailable")
		})
	}
}

func TestQueryEmbeddingAuthoritativeDispatch(t *testing.T) {
	for _, provider := range []bool{false, true} {
		name := "without-provider"
		p := &queryEmbeddingProvider{}
		var opts []Option
		if provider {
			name = "with-provider"
			opts = append(opts, WithProvider(p))
		}
		t.Run(name, func(t *testing.T) {
			e := queryEmbeddingEngine(t, opts...)
			// Text and graph anchors are allowed together; the supplied vector
			// points away from the provider output and the anchor vector.
			q := Query{Text: "alpha", Anchors: []string{"b"}, QueryEmbedding: &QueryEmbedding{Model: "synthetic-space", Vector: []float32{1, 0}}, Explain: true}
			r, _ := e.Retriever("dense")
			hits, err := r.Retrieve(testCtx, q, 10)
			if err != nil || len(hits) != 3 || hits[0].ID != "a" || hits[0].Score != 1 {
				t.Fatalf("raw supplied dispatch: %+v, %v", hits, err)
			}
			result := mustQuery(t, e, q)
			if len(result.Items) != 3 || result.Items[0].ID != "a" || len(result.Ranks["dense"]) != 3 || result.Ranks["dense"][0].ID != "a" {
				t.Fatalf("fused supplied dispatch: %+v", result)
			}
			q.Weights = weights("dense")
			q.Weights["graph"] = 1
			result = mustQuery(t, e, q)
			if len(result.Ranks["graph"]) == 0 || result.Ranks["graph"][0].ID != "b" || result.Ranks["dense"][0].ID != "a" {
				t.Fatalf("explicit graph anchors with a text/vector query: %+v", result.Ranks)
			}
			q.Text = ""
			q.Anchors = nil
			q.Weights = weights("dense")
			hits, err = r.Retrieve(testCtx, q, 10)
			if err != nil || len(hits) != 3 || hits[0].ID != "a" || hits[0].Score != 1 {
				t.Fatalf("raw vector-only dispatch: %+v, %v", hits, err)
			}
			result = mustQuery(t, e, q)
			if len(result.Items) != 3 || result.Items[0].ID != "a" {
				t.Fatalf("fused vector-only dispatch: %+v", result)
			}
			q.Weights = weights("temporal")
			mustQuery(t, e, q)
			if p.calls != 0 {
				t.Fatalf("supplied vector called provider %d times", p.calls)
			}
		})
	}
}

func TestQueryEmbeddingAbsentCompatibility(t *testing.T) {
	p := &queryEmbeddingProvider{}
	e := queryEmbeddingEngine(t, WithProvider(p))
	r, _ := e.Retriever("dense")
	hits, err := r.Retrieve(testCtx, Query{Text: "beta"}, 10)
	if err != nil || len(hits) != 3 || hits[0].ID != "b" || p.calls != 1 {
		t.Fatalf("absent vector provider path: %+v, calls=%d, %v", hits, p.calls, err)
	}
	result := mustQuery(t, e, Query{Text: "beta"})
	if len(result.Items) != 3 || result.Items[0].ID != "b" || p.calls != 2 {
		t.Fatalf("absent vector fused provider path: %+v, calls=%d", result, p.calls)
	}
	// Repeated anchor a is counted once, preserving the existing centroid.
	hits, err = r.Retrieve(testCtx, Query{Anchors: []string{"a", "a", "b"}}, 10)
	if err != nil || len(hits) != 3 || hits[0].ID != "c" || math.Abs(hits[0].Score-1) > 1e-6 || p.calls != 2 {
		t.Fatalf("absent vector anchor path: %+v, calls=%d, %v", hits, p.calls, err)
	}
	withoutProvider := queryEmbeddingEngine(t)
	r, _ = withoutProvider.Retriever("dense")
	hits, err = r.Retrieve(testCtx, Query{Text: "alpha", Anchors: []string{"a"}}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("provider=none absent vector compatibility: %+v, %v", hits, err)
	}
}

func TestQueryEmbeddingModelCheckedBeforeEmptyFilters(t *testing.T) {
	e := queryEmbeddingEngine(t)
	for _, path := range []string{"fused", "dense-disabled", "raw-dense"} {
		t.Run(path, func(t *testing.T) {
			q := Query{QueryEmbedding: &QueryEmbedding{Model: "other-space", Vector: []float32{1, 0}}, Filters: Filters{AllowedIDs: []string{}}}
			var err error
			if path == "raw-dense" {
				r, _ := e.Retriever("dense")
				_, err = r.Retrieve(testCtx, q, 10)
			} else {
				if path == "dense-disabled" {
					q.Weights = weights("temporal")
				}
				_, err = e.Query(testCtx, q)
			}
			requireEmbeddingError(t, err, "embedding_model_mismatch")
		})
	}
}

func TestQueryEmbeddingRequiresConfiguredModel(t *testing.T) {
	e := openTest(t, DefaultConfig())
	for _, model := range []string{"", "synthetic-space"} {
		t.Run("model="+model, func(t *testing.T) {
			_, err := e.Query(testCtx, Query{QueryEmbedding: &QueryEmbedding{Model: model, Vector: []float32{1, 0}}})
			requireEmbeddingError(t, err, "embedding_model_mismatch")
		})
	}
}
