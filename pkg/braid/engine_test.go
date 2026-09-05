package braid

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
var testCtx = context.Background()

type fixtureProvider struct{}

func (fixtureProvider) Model() string { return "fixture-v1" }
func (fixtureProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := [][]float32{}
	for _, text := range texts {
		v := make([]float32, 8)
		v[0] = 1
		for i, r := range text {
			v[i%8] += float32(r%17) / 17
		}
		out = append(out, v)
	}
	return out, nil
}
func openTest(t *testing.T, c Config, opts ...Option) *Engine {
	t.Helper()
	c.Store.Path = filepath.Join(t.TempDir(), "test.db")
	e, err := Open(testCtx, c, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
func node(id, typ, text string, cost float64) Node {
	return Node{ID: id, Type: typ, Text: text, TS: testTime, Cost: &cost}
}
func mustUpsert(t *testing.T, e *Engine, b Batch) {
	t.Helper()
	if err := e.Upsert(testCtx, b); err != nil {
		t.Fatal(err)
	}
}
func mustQuery(t *testing.T, e *Engine, q Query) Result {
	t.Helper()
	if q.At.IsZero() {
		q.At = testTime
	}
	r, err := e.Query(testCtx, q)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func weights(name string) map[string]float64 {
	m := map[string]float64{}
	for _, r := range retrieverNames {
		m[r] = 0
	}
	m[name] = 1
	return m
}

func TestMilestoneReplay200Nodes600Edges40Labels(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	c.Projection = []Projection{{On: "observed", Nodes: []NodeRule{{Type: "report", ID: "{{.id}}", Text: []string{"text"}}}, Edges: []EdgeRule{{From: "{{.id}}", To: "{{.}}", Type: "linked", Each: "neighbors"}}}}
	e := openTest(t, c, WithProvider(fixtureProvider{}))
	events := []Event{}
	labels := []Label{}
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("n:%03d", i)
		neighbors := []any{}
		for j := 1; j <= 3; j++ {
			neighbors = append(neighbors, fmt.Sprintf("n:%03d", (i+j)%200))
		}
		events = append(events, Event{ID: int64(i + 1), Name: "observed", TS: testTime.Add(-time.Duration(i) * time.Hour), Payload: map[string]any{"id": id, "text": fmt.Sprintf("report token%03d queue connection reset", i), "neighbors": neighbors}})
		if i < 40 {
			labels = append(labels, Label{QueryID: fmt.Sprint(i), Query: Query{Text: fmt.Sprintf("token%03d", i), Anchors: []string{id}, At: testTime}, Expected: []string{id}})
		}
	}
	if err := e.Ingest(testCtx, events); err != nil {
		t.Fatal(err)
	}
	if err := e.Reindex(testCtx); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"nodes": 200, "edges": 600, "events": 200, "embeddings": 200} {
		var got int
		if err := e.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d want %d (%v)", table, got, want, err)
		}
	}
	queries := []Query{{Text: "token007", Anchors: []string{"n:007"}, At: testTime, Explain: true}, {Anchors: []string{"n:007"}, At: testTime, Explain: true}, {Text: "queue", At: testTime, Explain: true}}
	before := [][]byte{}
	for _, q := range queries {
		res := mustQuery(t, e, q)
		if len(res.Items) == 0 {
			t.Fatal("empty result")
		}
		for _, it := range res.Items {
			if len(it.Why) == 0 {
				t.Fatal("missing explanation")
			}
		}
		b, _ := json.Marshal(res)
		before = append(before, b)
	}
	if err := e.Replay(testCtx); err != nil {
		t.Fatal(err)
	}
	for i, q := range queries {
		after, _ := json.Marshal(mustQuery(t, e, q))
		if string(after) != string(before[i]) {
			t.Fatalf("query %d changed after replay", i)
		}
	}
	for _, name := range retrieverNames {
		r, _ := e.Retriever(name)
		hits, err := r.Retrieve(testCtx, queries[0], 10)
		if err != nil || len(hits) == 0 {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(hits[0].Why[0], name+":") {
			t.Fatal("missing evidence", name)
		}
	}
	runs, err := e.Eval(testCtx, labels)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 7 || runs[0].Queries != 40 || runs[0].Metrics.Recall5 != 1 {
		t.Fatalf("unexpected evaluation: %+v", runs)
	}
}
func TestIngestAtomicIdempotentAndReplayFailure(t *testing.T) {
	c := DefaultConfig()
	c.Projection = []Projection{{On: "add", Nodes: []NodeRule{{ID: "{{.id}}", Type: "task", Text: []string{"title"}}}}}
	e := openTest(t, c)
	ev := Event{ID: 1, TS: testTime, Name: "add", Payload: map[string]any{"id": "one", "title": "original"}}
	if err := e.Ingest(testCtx, []Event{ev, {ID: 2, TS: testTime, Name: "add", Payload: map[string]any{}}}); err == nil {
		t.Fatal("expected invalid template failure")
	}
	ns, _ := e.nodes(testCtx)
	if len(ns) != 0 {
		t.Fatal("partial transaction persisted")
	}
	for i := 0; i < 2; i++ {
		if err := e.Ingest(testCtx, []Event{ev}); err != nil {
			t.Fatal(err)
		}
	}
	conflict := ev
	conflict.Payload = map[string]any{"id": "two"}
	if err := e.Ingest(testCtx, []Event{conflict}); err == nil {
		t.Fatal("expected conflict")
	}
	e.cfg.Projection[0].Nodes[0].ID = "{{.missing}}"
	if err := e.Replay(testCtx); err == nil {
		t.Fatal("expected replay error")
	}
	ns, _ = e.nodes(testCtx)
	if len(ns) != 1 || ns[0].ID != "one" {
		t.Fatal("failed replay damaged prior projection")
	}
	e.cfg.Projection[0].Nodes[0].ID = "{{.id}}"
	if err := e.Replay(testCtx); err != nil {
		t.Fatal("retry after failed replay", err)
	}
}
func TestFTSUpdatesFiltersAndLiteralSyntax(t *testing.T) {
	e := openTest(t, DefaultConfig())
	nodes := []Node{}
	for i := 0; i < 20; i++ {
		nodes = append(nodes, node(fmt.Sprintf("other%d", i), "file", "ConnectionResetError", 1))
	}
	nodes = append(nodes, node("match", "report", "ConnectionResetError café", 1))
	mustUpsert(t, e, Batch{Nodes: nodes})
	r, _ := e.Retriever("lexical")
	hits, err := r.Retrieve(testCtx, Query{Text: "ConnectionResetError", Filters: Filters{Types: []string{"report"}}}, 1)
	if err != nil || len(hits) != 1 || hits[0].ID != "match" {
		t.Fatalf("filter before limit: %+v %v", hits, err)
	}
	for _, text := range []string{"cafe", `ConnectionResetError: " ( OR *`} {
		hits, err = r.Retrieve(testCtx, Query{Text: text}, 30)
		if err != nil || len(hits) == 0 {
			t.Fatalf("literal/diacritics %s: %v", text, err)
		}
	}
	mustUpsert(t, e, Batch{Nodes: []Node{node("match", "report", "replacement", 1)}})
	hits, err = r.Retrieve(testCtx, Query{Text: "cafe"}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("stale FTS entry", hits, err)
	}
	hits, err = r.Retrieve(testCtx, Query{Text: "replacement", Filters: Filters{Since: testTime.Add(time.Second)}}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("since filter")
	}
}
func TestBudgetSkipZeroAndBoost(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("temporal")
	c.Defaults.MMRLambda = 1
	e := openTest(t, c)
	n := node("expensive", "report", "x", 100)
	n.Attrs = map[string]any{"status": "failed"}
	mustUpsert(t, e, Batch{Nodes: []Node{n, node("small", "file", "y", 2), node("free", "decision", "z", 0)}})
	r := mustQuery(t, e, Query{Budget: &Budget{Max: 2}, Boosts: []Boost{{Type: "report", Factor: 1000, Where: "attrs.status == failed"}}})
	if r.TotalCost != 2 || len(r.Items) != 2 || r.DroppedForBudget != 1 {
		t.Fatalf("budget: %+v", r)
	}
	r = mustQuery(t, e, Query{Budget: &Budget{Max: 0}})
	if len(r.Items) != 1 || r.Items[0].ID != "free" {
		t.Fatal("zero budget", r)
	}
	_, err := e.Query(testCtx, Query{Budget: &Budget{Max: -1}})
	if err == nil {
		t.Fatal("negative budget")
	}
}
func TestGraphDirectionHopsAndDanglingMass(t *testing.T) {
	c := DefaultConfig()
	c.Graph.Directed = []string{"oneway"}
	c.Graph.MaxHops = 1
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "n", "", 1), node("b", "n", "", 1), node("c", "n", "", 1)}, Edges: []Edge{{Src: "a", Dst: "b", Type: "oneway", Weight: 1, TS: testTime}, {Src: "b", Dst: "c", Type: "oneway", Weight: 1, TS: testTime}}})
	r, _ := e.Retriever("graph")
	hits, err := r.Retrieve(testCtx, Query{Anchors: []string{"b"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	mass := 0.
	for _, h := range hits {
		if h.ID == "a" {
			t.Fatal("traversed directed edge backwards")
		}
		mass += h.Score
	}
	if len(hits) != 2 || math.Abs(mass-1) > 1e-12 {
		t.Fatal("mass not conserved", hits, mass)
	}
	hits, _ = r.Retrieve(testCtx, Query{Anchors: []string{"a"}, Filters: Filters{ExcludeIDs: []string{"a"}}}, 10)
	if len(hits) != 1 || hits[0].ID != "b" {
		t.Fatal("hops/filter", hits)
	}
	hits, _ = r.Retrieve(testCtx, Query{Anchors: []string{"missing"}}, 10)
	if len(hits) != 0 {
		t.Fatal("missing anchor")
	}
}
func TestTemporalHalfLifeAndAnchorOnlyDense(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	e := openTest(t, c, WithProvider(fixtureProvider{}))
	old := node("old", "n", "old", 1)
	old.TS = testTime.Add(-72 * time.Hour)
	mustUpsert(t, e, Batch{Nodes: []Node{node("now", "n", "new", 1), old}, Embeddings: []Embedding{{NodeID: "now", Model: "fixture-v1", Vec: []float32{1, 0}}, {NodeID: "old", Model: "fixture-v1", Vec: []float32{0, 1}}}})
	r, _ := e.Retriever("temporal")
	h, _ := r.Retrieve(testCtx, Query{At: testTime}, 10)
	if h[0].Score != 1 || math.Abs(h[1].Score-.5) > 1e-12 {
		t.Fatal("half life", h)
	}
	r, _ = e.Retriever("dense")
	h, err := r.Retrieve(testCtx, Query{Anchors: []string{"now", "now"}}, 10)
	if err != nil || h[0].ID != "now" || h[0].Score != 1 {
		t.Fatal("centroid", h, err)
	}
	_, err = r.Retrieve(testCtx, Query{Text: "provider returns eight dimensions"}, 10)
	if err == nil {
		t.Fatal("expected dimension error")
	}
	if err = e.Upsert(testCtx, Batch{Embeddings: []Embedding{{NodeID: "old", Model: "fixture-v1", Vec: []float32{1, 2, 3}}}}); err == nil {
		t.Fatal("accepted mixed dimensions")
	}
	mustUpsert(t, e, Batch{Nodes: []Node{node("now", "n", "changed", 1)}})
	vs, _ := e.vectors(testCtx)
	if _, ok := vs["now"]; ok {
		t.Fatal("text update retained stale embedding")
	}
}
func TestMetrics(t *testing.T) {
	m := measure([]string{"x", "a", "a", "b"}, []string{"a", "b"})
	if m.MRR != .5 || m.Recall5 != 1 || m.Recall20 != 1 {
		t.Fatal(m)
	}
	if m.NDCG10 <= 0 || m.NDCG10 >= 1 {
		t.Fatal(m)
	}
	if !reflect.DeepEqual(measure(nil, nil), Metrics{}) {
		t.Fatal("empty metrics")
	}
}
func TestCostFieldAndCustomCost(t *testing.T) {
	c := DefaultConfig()
	c.Index.CostField = "tokens"
	e := openTest(t, c)
	n := node("a", "n", "abc", 1)
	n.Cost = nil
	n.Attrs = map[string]any{"tokens": 3.5}
	mustUpsert(t, e, Batch{Nodes: []Node{n}})
	r := mustQuery(t, e, Query{})
	if r.TotalCost != 3.5 {
		t.Fatal(r)
	}
	e.cost = func(Node) (float64, error) { return math.NaN(), nil }
	if _, err := e.Query(testCtx, Query{}); err == nil {
		t.Fatal("invalid custom cost accepted")
	}
}

func TestMMRChangesShortlistBeforePacking(t *testing.T) {
	c := DefaultConfig()
	c.CandidateLimit = 2
	c.Defaults.Weights = weights("lexical")
	c.Defaults.Weights["temporal"] = 1
	c.Defaults.MMRLambda = 0
	e := openTest(t, c)
	a := node("a", "report", "error", 1)
	b := node("b", "report", "error", 1)
	d := node("d", "decision", "unrelated", 1)
	a.TS = testTime.Add(-time.Hour)
	b.TS = a.TS
	mustUpsert(t, e, Batch{Nodes: []Node{a, b, d}})
	r := mustQuery(t, e, Query{Text: "error"})
	if r.Considered != 3 || len(r.Items) != 2 {
		t.Fatal(r)
	}
	ids := map[string]bool{}
	for _, it := range r.Items {
		ids[it.ID] = true
	}
	if !ids["d"] {
		t.Fatal("MMR shortlist did not retain diverse type", r)
	}
}
func TestModelChangeRequiresReindex(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	e := openTest(t, c, WithProvider(fixtureProvider{}))
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "n", "abc", 1)}, Embeddings: []Embedding{{NodeID: "a", Model: "old-model", Vec: []float32{1, 2}}}})
	if _, err := e.Query(testCtx, Query{Anchors: []string{"a"}}); err == nil {
		t.Fatal("old embedding model silently accepted")
	}
	if err := e.Reindex(testCtx); err != nil {
		t.Fatal(err)
	}
	mustQuery(t, e, Query{Anchors: []string{"a"}})
}
func TestProjectionScalarAndRoot(t *testing.T) {
	for _, v := range []any{false, 0, ""} {
		got, err := render(`{{$event.id}}:{{.}}`, map[string]any{"id": "root"}, v)
		if err != nil || got != "root:"+fmt.Sprint(v) {
			t.Fatal(got, err)
		}
	}
}

func TestLibraryPayloadNormalizesBeforeProjection(t *testing.T) {
	c := DefaultConfig()
	c.Projection = []Projection{{On: "add", Nodes: []NodeRule{{ID: "{{.id}}", Type: "task"}}, Edges: []EdgeRule{{From: "{{.id}}", To: "{{.}}", Type: "link", Each: "targets"}}}}
	e := openTest(t, c)
	err := e.Ingest(testCtx, []Event{{ID: 1, TS: testTime, Name: "add", Payload: map[string]any{"id": "a", "targets": []string{"b", "c"}}}})
	if err != nil {
		t.Fatal(err)
	}
	before := mustQuery(t, e, Query{})
	if err = e.Replay(testCtx); err != nil {
		t.Fatal(err)
	}
	after := mustQuery(t, e, Query{})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("library payload changed on replay")
	}
}
