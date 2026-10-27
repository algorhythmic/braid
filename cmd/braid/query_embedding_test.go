package main

// Package A: https://github.com/algorhythmic/muninn/blob/8bdda8b/docs/braid-integration/A-EMBEDDING-COMPATIBILITY.md

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/algorhythmic/braid/pkg/braid"
)

func queryEmbeddingProtocolEngine(t *testing.T, published bool) *braid.Engine {
	t.Helper()
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	c.Embed.Model = "synthetic-space"
	e, err := braid.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if published {
		err = e.Upsert(context.Background(), braid.Batch{
			Nodes:      []braid.Node{{ID: "a", Type: "task", Text: "synthetic needle", TS: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}},
			Embeddings: []braid.Embedding{{NodeID: "a", Model: c.Embed.Model, Vec: []float32{1, 0}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func queryEmbeddingRequest(method, query string) []byte {
	if method == "export_context" {
		return []byte(fmt.Sprintf(`{"id":1,"method":"export_context","export":{"query":%s,"output":{"max_bytes":4096}}}`, query))
	}
	return []byte(fmt.Sprintf(`{"id":1,"method":%q,"query":%s}`, method, query))
}

func TestQueryEmbeddingProtocolCapabilityAndMethods(t *testing.T) {
	e := queryEmbeddingProtocolEngine(t, true)
	ready := false
	r := handleRequest(context.Background(), e, []byte(`{"id":1,"method":"hello","versions":[1],"required_capabilities":["query_embedding"]}`), &ready)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	advertised := false
	for _, c := range r.Result.(map[string]any)["capabilities"].([]string) {
		if c == "query_embedding" {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("query_embedding not advertised")
	}
	for _, method := range []string{"query", "explain", "export_context"} {
		for _, query := range []string{
			`{"text":"needle","query_embedding":{"model":"synthetic-space","vector":[1,0]}}`,
			`{"text":"needle","anchors":["a"],"query_embedding":{"model":"synthetic-space","vector":[1,0]}}`,
			`{"text":"needle","query_embedding":null}`,
			`{"text":"needle"}`,
		} {
			t.Run(method+query, func(t *testing.T) {
				r := handleRequest(context.Background(), e, queryEmbeddingRequest(method, query), &ready)
				if r.Error != nil {
					t.Fatal(r.Error)
				}
			})
		}
	}
}

func TestQueryEmbeddingProtocolStrictShape(t *testing.T) {
	for _, method := range []string{"query", "explain", "export_context"} {
		for _, embedding := range []string{
			`{"Model":"synthetic-space","vector":[1,0]}`,
			`{"model":"synthetic-space","Vector":[1,0]}`,
			`{"model":"synthetic-space","vector":[1,0],"extra":true}`,
			`{"model":"synthetic-space","model":"other","vector":[1,0]}`,
			`{"model":"synthetic-space","vector":[1,0],"vector":[0,1]}`,
			`{"model":null,"vector":[1,0]}`,
			`{"model":1,"vector":[1,0]}`,
			`{"model":"synthetic-space","vector":[null,0]}`,
			`{"model":"synthetic-space","vector":["1",0]}`,
			`{"model":"synthetic-space","vector":[1e39,0]}`,
			`[]`, `"vector"`,
		} {
			t.Run(method+embedding, func(t *testing.T) {
				_, problem := validateRequest(queryEmbeddingRequest(method, `{"query_embedding":`+embedding+`}`))
				if problem == nil || problem.Code != "invalid_request" {
					t.Fatalf("got %+v", problem)
				}
			})
		}
	}
}

// JSON has no NaN or infinity literals. Such input fails before typed engine
// validation; a numeric overflow of float32 is an invalid request above.
func TestQueryEmbeddingProtocolNonfiniteJSON(t *testing.T) {
	for _, value := range []string{"NaN", "Infinity", "-Infinity"} {
		t.Run(value, func(t *testing.T) {
			_, problem := validateRequest(queryEmbeddingRequest("query", `{"query_embedding":{"model":"synthetic-space","vector":[`+value+`,0]}}`))
			if problem == nil || problem.Code != "parse_error" {
				t.Fatalf("got %+v", problem)
			}
		})
	}
}

func TestQueryEmbeddingProtocolValidationCodes(t *testing.T) {
	for _, method := range []string{"query", "explain", "export_context"} {
		t.Run(method, func(t *testing.T) {
			e := queryEmbeddingProtocolEngine(t, true)
			ready := true
			for _, tc := range []struct{ name, query, code string }{
				{"empty", `{"query_embedding":{"model":"synthetic-space","vector":[]}}`, "embedding_empty_vector"},
				{"missing_vector", `{"query_embedding":{"model":"synthetic-space"}}`, "embedding_empty_vector"},
				{"null_vector", `{"query_embedding":{"model":"synthetic-space","vector":null}}`, "embedding_empty_vector"},
				{"zero", `{"query_embedding":{"model":"synthetic-space","vector":[0,0]}}`, "embedding_zero_vector"},
				{"model", `{"query_embedding":{"model":"other","vector":[1,0]}}`, "embedding_model_mismatch"},
				{"missing_model", `{"query_embedding":{"vector":[1,0]}}`, "embedding_model_mismatch"},
				{"dimension", `{"query_embedding":{"model":"synthetic-space","vector":[1]}}`, "embedding_dimension_mismatch"},
				{"dimension_empty_filter", `{"filters":{"allowed_ids":[]},"query_embedding":{"model":"synthetic-space","vector":[1]}}`, "embedding_dimension_mismatch"},
				{"model_empty_filter", `{"filters":{"allowed_ids":[]},"query_embedding":{"model":"other","vector":[1,0]}}`, "embedding_model_mismatch"},
				{"anchors", `{"anchors":["a"],"query_embedding":{"model":"synthetic-space","vector":[1,0]}}`, "embedding_ambiguous_anchors"},
				{"dense_disabled", `{"weights":{"dense":0},"query_embedding":{"model":"synthetic-space","vector":[0,0]}}`, "embedding_zero_vector"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := handleRequest(context.Background(), e, queryEmbeddingRequest(method, tc.query), &ready)
					if r.Error == nil || r.Error.Code != tc.code || r.Result != nil {
						t.Fatalf("got %+v, want %s", r, tc.code)
					}
				})
			}
			empty := queryEmbeddingProtocolEngine(t, false)
			r := handleRequest(context.Background(), empty, queryEmbeddingRequest(method, `{"query_embedding":{"model":"synthetic-space","vector":[1,0]}}`), &ready)
			if r.Error == nil || r.Error.Code != "embedding_dimension_unavailable" {
				t.Fatalf("got %+v", r)
			}
		})
	}
}
