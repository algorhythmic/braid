package braid

import (
	"reflect"
	"testing"
	"time"
)

func TestTextRedundancyEvidence(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want float64
	}{{"Queue, RESET!", "reset queue", 1}, {"send invoice", "repair queue", 0}, {"queue reset", "queue retry", 1.0 / 3}, {"", "", 0}, {"!!!", "?", 0}} {
		got := termOverlap(redundancyTerms(tc.a), redundancyTerms(tc.b))
		if got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
func diversityFixture(t *testing.T, lambda float64, vector bool) *Engine {
	c := DefaultConfig()
	c.CandidateLimit = 2
	c.Defaults.MMRLambda = lambda
	c.Defaults.Weights = weights("lexical")
	c.Defaults.Weights["temporal"] = 1
	opts := []Option{}
	if vector {
		c.Embed.Model = "fixture-v1"
		opts = append(opts, WithProvider(fixtureProvider{}))
	}
	e := openTest(t, c, opts...)
	a := node("a", "task", "queue", 1)
	b := node("b", "task", "queue", 1)
	d := node("d", "other", "invoice", 1)
	a.TS = testTime.Add(-time.Hour)
	b.TS = a.TS
	b.Attrs = map[string]any{"boost": true}
	batch := Batch{Nodes: []Node{a, b, d}}
	if vector {
		batch.Embeddings = []Embedding{{NodeID: "a", Model: "fixture-v1", Vec: []float32{1, 0}}, {NodeID: "b", Model: "fixture-v1", Vec: []float32{1, 0}}, {NodeID: "d", Model: "fixture-v1", Vec: []float32{0, 1}}}
	}
	mustUpsert(t, e, batch)
	return e
}
func resultIDs(r Result) []string {
	ids := []string{}
	for _, it := range r.Items {
		ids = append(ids, it.ID)
	}
	return ids
}
func TestNormalizedRelevanceAndWeightScale(t *testing.T) {
	for _, vector := range []bool{false, true} {
		e := diversityFixture(t, .7, vector)
		q := Query{Text: "queue", Boosts: []Boost{{Where: "attrs.boost == true", Factor: 1.95}}}
		r := mustQuery(t, e, q)
		if !reflect.DeepEqual(resultIDs(r), []string{"a", "b"}) {
			t.Fatalf("strong relevance lost to diversity: %+v", r)
		}
		q.Weights = weights("lexical")
		q.Weights["lexical"] = 100
		q.Weights["temporal"] = 100
		if got := resultIDs(mustQuery(t, e, q)); !reflect.DeepEqual(got, resultIDs(r)) {
			t.Fatal("common weight scaling changed MMR", got)
		}
	}
}
func TestRedundancyIgnoresTypeAndKeepsDistinctText(t *testing.T) {
	e := diversityFixture(t, .4, false)
	q := Query{Text: "queue"}
	before := resultIDs(mustQuery(t, e, q))
	if !contains(before, "d") {
		t.Fatal("duplicate not penalized")
	}
	nodes, err := e.nodes(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range nodes {
		nodes[i].Type = "same-type"
	}
	mustUpsert(t, e, Batch{Nodes: nodes})
	after := resultIDs(mustQuery(t, e, q))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("type labels affected redundancy", before, after)
	}
}
