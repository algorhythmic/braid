package braid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHardEligibilityBeforeEveryRetrieverLimit(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	c.CandidateLimit = 1
	e := openTest(t, c, WithProvider(fixtureProvider{}))
	batch := Batch{}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("n%02d", i)
		n := node(id, "binding", "find files", 1)
		n.Attrs = map[string]any{"app": "editor", "enabled": false, "modes": []string{"i"}}
		if i == 49 {
			n.Attrs["enabled"] = true
			n.Attrs["modes"] = []string{"n", "v"}
			n.Attrs["nullable"] = nil
		}
		batch.Nodes = append(batch.Nodes, n)
		batch.Embeddings = append(batch.Embeddings, Embedding{NodeID: id, Model: c.Embed.Model, Vec: []float32{1, 0, 0, 0, 0, 0, 0, 0}})
		if i > 0 {
			batch.Edges = append(batch.Edges, Edge{Src: "n00", Dst: id, Type: "related", Weight: 1, TS: testTime})
		}
	}
	mustUpsert(t, e, batch)
	pred := &Predicate{Op: "and", Children: []Predicate{{Op: "eq", Path: "app", Value: "editor"}, {Op: "contains", Path: "modes", Value: "n"}, {Op: "eq", Path: "enabled", Value: true}, {Op: "exists", Path: "nullable", Value: true}}}
	for _, name := range retrieverNames {
		r, _ := e.Retriever(name)
		q := Query{Text: "find files", Anchors: []string{"n00"}, At: testTime, Filters: Filters{Predicate: pred}}
		hits, err := r.Retrieve(testCtx, q, 1)
		if err != nil || len(hits) != 1 || hits[0].ID != "n49" {
			t.Fatalf("%s: %+v %v", name, hits, err)
		}
		for _, filter := range []Filters{{AllowedIDs: []string{}}, {AllowedIDs: []string{"n49"}, ExcludeIDs: []string{"n49"}}, {Predicate: &Predicate{Op: "eq", Path: "enabled", Value: "true"}}, {Predicate: &Predicate{Op: "eq", Path: "absent", Value: nil}}} {
			q.Filters = filter
			hits, err = r.Retrieve(testCtx, q, 1)
			if err != nil || len(hits) != 0 {
				t.Fatalf("%s filter %+v: %+v %v", name, filter, hits, err)
			}
		}
		q.Filters = Filters{Predicate: &Predicate{Op: "or", Children: []Predicate{{Op: "eq", Path: "nullable", Value: nil}, {Op: "in", Path: "app", Values: []any{"browser", 3}}}}}
		hits, err = r.Retrieve(testCtx, q, 1)
		if err != nil || len(hits) != 1 || hits[0].ID != "n49" {
			t.Fatalf("null/in/or %s: %+v %v", name, hits, err)
		}
	}
	b, _ := json.Marshal(Query{Filters: Filters{AllowedIDs: []string{}}})
	var q Query
	_ = json.Unmarshal(b, &q)
	if q.Filters.AllowedIDs == nil {
		t.Fatal("empty allowed set lost in round trip")
	}
}

func TestPredicateTypesAndValidation(t *testing.T) {
	e := openTest(t, DefaultConfig())
	n := node("n", "n", "match", 1)
	n.Attrs = map[string]any{"number": 1, "bool": true, "nested": map[string]any{"value": "N"}, "array": []any{nil, 1, "1", true}}
	mustUpsert(t, e, Batch{Nodes: []Node{n}})
	for _, tc := range []struct {
		p    Predicate
		want bool
	}{
		{Predicate{Op: "eq", Path: "number", Value: 1}, true}, {Predicate{Op: "eq", Path: "number", Value: true}, false},
		{Predicate{Op: "eq", Path: "bool", Value: 1}, false}, {Predicate{Op: "eq", Path: "nested.value", Value: "N"}, true},
		{Predicate{Op: "eq", Path: "nested.value", Value: "n"}, false}, {Predicate{Op: "contains", Path: "number", Value: 1}, false},
		{Predicate{Op: "contains", Path: "array", Value: nil}, true}, {Predicate{Op: "exists", Path: "absent", Value: false}, true},
		{Predicate{Op: "in", Path: "number", Values: []any{}}, false},
	} {
		r := mustQuery(t, e, Query{Text: "match", Weights: weights("lexical"), Filters: Filters{Predicate: &tc.p}})
		if (len(r.Items) > 0) != tc.want {
			t.Fatalf("%+v: %+v", tc, r)
		}
	}
	for _, p := range []Predicate{{Op: "sql", Path: "number"}, {Op: "eq", Path: "x') OR 1=1 --", Value: 1}, {Op: "eq", Path: "number", Value: []int{1}}, {Op: "and"}, {Op: "exists", Path: "number"}} {
		if _, err := e.Query(testCtx, Query{Filters: Filters{Predicate: &p}}); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestExactReadsAndEarlyRevisionCheck(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	p := &countingProvider{}
	e := openTest(t, c, WithProvider(p))
	nodes := []Node{}
	for _, id := range []string{"n", "N", "?", "<C-w>v"} {
		n := node(id, "binding", "key", 1)
		n.Attrs = map[string]any{"key": id}
		nodes = append(nodes, n)
	}
	mustUpsert(t, e, Batch{Nodes: nodes})
	got, err := e.GetMany(testCtx, GetRequest{DatasetID: c.DatasetID, IDs: []string{"?", "N", "n", "?", "<C-w>v", "missing"}, ExpectedRevision: revision(1)})
	if err != nil || len(got.Nodes) != 4 || !reflect.DeepEqual(got.MissingIDs, []string{"missing"}) {
		t.Fatal(got, err)
	}
	for i, id := range []string{"?", "N", "n", "<C-w>v"} {
		if got.Nodes[i].ID != id || got.Nodes[i].Attrs["key"] != id {
			t.Fatal(got)
		}
	}
	for _, id := range []string{"", "other"} {
		if _, err = e.GetMany(testCtx, GetRequest{DatasetID: id, IDs: []string{"n"}}); !errors.Is(err, ErrDatasetMismatch) {
			t.Fatal(err)
		}
	}
	if _, err = e.GetMany(testCtx, GetRequest{DatasetID: c.DatasetID, IDs: []string{"n"}, ExpectedRevision: revision(0)}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal(err)
	}
	if _, err = e.Query(testCtx, Query{Text: "find", ExpectedRevision: revision(0)}); !errors.Is(err, ErrRevisionConflict) || p.calls != 0 {
		t.Fatal(err, p.calls)
	}
	dense, _ := e.Retriever("dense")
	if _, err = dense.Retrieve(testCtx, Query{Text: "find", ExpectedRevision: revision(0)}, 10); !errors.Is(err, ErrRevisionConflict) || p.calls != 0 {
		t.Fatal(err, p.calls)
	}
}

func TestConcurrentExactReadsNeverMixGenerations(t *testing.T) {
	c := DefaultConfig()
	e := openTest(t, c)
	other, err := Open(testCtx, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "n", "0", 1), node("b", "n", "0", 1)}})
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 30; i++ {
			_, er := other.Apply(testCtx, Update{DatasetID: c.DatasetID, ExpectedRevision: revision(int64(i)), Upsert: Batch{Nodes: []Node{node("a", "n", fmt.Sprint(i), 1), node("b", "n", fmt.Sprint(i), 1)}}})
			if er != nil {
				done <- er
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 100; i++ {
		state, er := e.Dataset(testCtx)
		if er != nil {
			t.Fatal(er)
		}
		r, er := e.GetMany(testCtx, GetRequest{DatasetID: c.DatasetID, IDs: []string{"a", "b"}, ExpectedRevision: &state.Revision})
		if errors.Is(er, ErrRevisionConflict) {
			continue
		}
		if er != nil || len(r.Nodes) != 2 || r.Dataset != state || r.Nodes[0].Text != r.Nodes[1].Text || r.Nodes[0].Text != fmt.Sprint(r.Dataset.Revision-1) {
			t.Fatal(r, er)
		}
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRequiredBundlesSharedCyclesAndFailures(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "action", "find", 1), node("b", "action", "find", 1), node("prefix", "context", "custom leader", 1), node("check", "context", "verify focus", 0)}})
	requires := map[string][]string{"a": {"prefix"}, "b": {"prefix"}, "prefix": {"check"}, "check": {"prefix"}}
	q := Query{Text: "find", Requires: requires, Budget: &Budget{Max: 3}}
	r := mustQuery(t, e, q)
	if len(r.Items) != 4 || r.TotalCost != 3 || r.Accounting.Required != 1 || r.Accounting.Content != 2 {
		t.Fatal(r)
	}
	q.Budget.Max = 1
	r = mustQuery(t, e, q)
	if len(r.Items) != 0 || len(r.Rejections) != 2 {
		t.Fatal(r)
	}
	q.Budget.Max = 3
	q.Filters.ExcludeIDs = []string{"prefix"}
	r = mustQuery(t, e, q)
	if len(r.Items) != 0 || r.Rejections[0].Reason != "required_context_unavailable" {
		t.Fatal(r)
	}
	q.Filters = Filters{}
	q.Requires = map[string][]string{"a": {"missing"}, "b": {"missing"}}
	r = mustQuery(t, e, q)
	if len(r.Items) != 0 {
		t.Fatal(r)
	}
	q.Requires = requires
	q.MandatoryIDs = []string{"a"}
	q.Budget.Max = 1
	r = mustQuery(t, e, q)
	if len(r.Items) != 0 || r.AbstentionReason != "infeasible_budget" {
		t.Fatal(r)
	}
	q.Budget = &Budget{Max: 2, Fixed: 3}
	r = mustQuery(t, e, q)
	if r.TotalCost > 2 || r.AbstentionReason != "infeasible_budget" {
		t.Fatal(r)
	}
}

func TestThreeCandidateDiversitySurvivesPacking(t *testing.T) {
	c := DefaultConfig()
	c.CandidateLimit = 3
	c.Defaults.MMRLambda = .4
	e := openTest(t, c)
	byID := map[string]Node{"a": node("a", "n", "duplicate", 1), "b": node("b", "n", "duplicate", 1), "c": node("c", "n", "distinct", 1)}
	candidates := []Hit{{ID: "a", Score: 1}, {ID: "b", Score: .99}, {ID: "c", Score: .9}}
	r := Result{Items: []Item{}}
	if err := e.pack(testCtx, Query{Budget: &Budget{Max: 2}}, candidates, byID, map[string][]float32{}, nil, &r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resultIDs(r), []string{"a", "c"}) {
		t.Fatal(r)
	}
	// A skipped oversized duplicate must not penalize B.
	huge := 100.
	a := byID["a"]
	a.Cost = &huge
	byID["a"] = a
	r = Result{Items: []Item{}}
	if err := e.pack(testCtx, Query{Budget: &Budget{Max: 1}}, candidates, byID, map[string][]float32{}, nil, &r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resultIDs(r), []string{"b"}) {
		t.Fatal(r)
	}
}

func TestBoundedProjectedContext(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	e := openTest(t, c)
	n := node("a", "action", "find", 1)
	n.Attrs = map[string]any{"large": strings.Repeat("<&", 10000), "key": "<C-w>v"}
	mustUpsert(t, e, Batch{Nodes: []Node{n, node("prefix", "context", "custom prefix", 1)}})
	req := ExportRequest{Query: Query{Text: "find", Requires: map[string][]string{"a": {"prefix"}}, Budget: &Budget{Max: 10, Fixed: 1, Unit: "widgets", Estimator: "adapter-v1"}}, Output: Output{MaxBytes: 300}}
	r, err := e.Export(testCtx, req)
	if err != nil || string(r.Context) != "[]" || r.ContextBytes > 300 {
		t.Fatal(r, err)
	}
	req.Output.Attrs = []string{"key"}
	r, err = e.Export(testCtx, req)
	if err != nil || r.Abstained || r.ContextBytes != len(r.Context) || r.ContextBytes > 300 || r.TotalCost != 3 || r.Accounting.Unit != "widgets" {
		t.Fatal(r, err)
	}
	req.Output.MaxBytes = r.ContextBytes
	r, err = e.Export(testCtx, req)
	if err != nil || r.Abstained {
		t.Fatal(r, err)
	}
	req.Output.MaxBytes--
	req.Query.MandatoryIDs = []string{"a"}
	r, err = e.Export(testCtx, req)
	if err != nil || r.AbstentionReason != "infeasible_budget" || string(r.Context) != "[]" {
		t.Fatal(r, err)
	}
	req.Output = Output{Fields: []string{"id", "why", "attrs"}, MaxBytes: 300}
	r, err = e.Export(testCtx, req)
	if err != nil || r.ContextBytes > 300 {
		t.Fatal(r, err)
	}
}

func TestLexicalPlanDoesNotReadGraphOrUnusedVectors(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	c.Defaults.MMRLambda = 1
	c.Embed.Model = "fixture-v1"
	e := openTest(t, c)
	nodes := []Node{}
	for i := 0; i < 100; i++ {
		nodes = append(nodes, node(fmt.Sprint(i), "n", fmt.Sprintf("token%d", i), 1))
	}
	mustUpsert(t, e, Batch{Nodes: nodes})
	// Missing backing tables make accidental reads fail independently of instrumentation.
	if _, err := e.db.Exec(`DROP TABLE edges; DROP TABLE embeddings`); err != nil {
		t.Fatal(err)
	}
	r := mustQuery(t, e, Query{Text: "token42", Trace: true})
	if len(r.Items) != 1 || r.Stages["nodes"].Rows != 1 || r.Stages["vectors"].Rows != 0 || r.Stages["graph_ids"].Rows != 0 {
		t.Fatal(r)
	}
}

func TestVectorDiversityLoadsOnlyCandidatesAndMatchesDensePlan(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	c.Embed.Model = "fixture-v1"
	c.CandidateLimit = 10
	e := openTest(t, c, WithProvider(fixtureProvider{}))
	batch := Batch{}
	for i := 0; i < 100; i++ {
		text := "unrelated"
		if i < 3 {
			text = "find files"
		}
		id := fmt.Sprint(i)
		batch.Nodes = append(batch.Nodes, node(id, "binding", text, 1))
		batch.Embeddings = append(batch.Embeddings, Embedding{NodeID: id, Model: c.Embed.Model, Vec: []float32{1, float32(i)}})
	}
	mustUpsert(t, e, batch)
	r := mustQuery(t, e, Query{Text: "find", Trace: true, Budget: &Budget{Max: 2}})
	if r.Stages["vectors"].Rows != 3 || r.Stages["nodes"].Rows != 3 {
		t.Fatal(r.Stages)
	}
	// A temporal stage with zero boost on its extra hits must not change the
	// vector-based diversity decision for the three lexical candidates.
	q := Query{Text: "find", Weights: weights("lexical"), Budget: &Budget{Max: 2}, RequireEvidence: true}
	q.Weights["temporal"] = 1e-15
	full := mustQuery(t, e, q)
	if !reflect.DeepEqual(resultIDs(r), resultIDs(full)) {
		t.Fatal(resultIDs(r), resultIDs(full))
	}
}

func TestEligibilityAndNoAnswerDiagnostics(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	e := openTest(t, c)
	n := node("extra", "binding", "find files", 1)
	n.Attrs = map[string]any{"enabled": false}
	mustUpsert(t, e, Batch{Nodes: []Node{n}})
	q := Query{Text: "find files", Filters: Filters{Predicate: &Predicate{Op: "eq", Path: "enabled", Value: true}}}
	r := mustQuery(t, e, q)
	if !r.Abstained || r.LexicalStatus != "filtered_out" || r.AbstentionReason != "eligibility" {
		t.Fatal(r)
	}
	q.Text = "start recording"
	r = mustQuery(t, e, q)
	if !r.Abstained || r.LexicalStatus != "no_matches" {
		t.Fatal(r)
	}
}

func TestRequiredContextContributesDiversityAndStrictFractionalBudget(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	c.Defaults.MMRLambda = .4
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "n", "find alpha", .1), node("b", "n", "find beta", .2), node("c", "n", "find gamma", .3), node("prefix", "n", "find beta", 0)}})
	r := mustQuery(t, e, Query{Text: "find", MandatoryIDs: []string{"prefix"}, Budget: &Budget{Max: .4}, Requires: map[string][]string{"a": {"prefix"}}})
	if !contains(resultIDs(r), "a") || !contains(resultIDs(r), "c") || contains(resultIDs(r), "b") || r.TotalCost > .4 {
		t.Fatal(r)
	}
}

type deadlineProvider struct{}

func (deadlineProvider) Model() string { return "fixture-v1" }
func (deadlineProvider) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestQueryCancellationDuringEngineWaitAndProvider(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	e := openTest(t, c, WithProvider(deadlineProvider{}))
	for _, lock := range []bool{true, false} {
		if lock {
			e.mu.Lock()
		}
		ctx, cancel := context.WithTimeout(testCtx, 20*time.Millisecond)
		start := time.Now()
		_, err := e.Query(ctx, Query{Text: "find"})
		cancel()
		if lock {
			e.mu.Unlock()
		}
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatal(err, time.Since(start))
		}
	}
	mustQuery(t, e, Query{Weights: weights("lexical"), Text: "still usable"})
}

func TestGridCannotChooseForbiddenHigherRecall(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("allowed", "n", "find", 1), node("disabled", "n", "find", 1)}})
	zero := 0.
	labels := []Label{{QueryID: "disabled-extra", Query: Query{Text: "find", At: testTime}, Expected: []string{"allowed"}, ForbiddenIDs: []string{"disabled"}}}
	g := Grid{Weights: map[string][]float64{"lexical": {1, 0}}, Constraints: &EvalConstraints{MaxForbiddenHitRate: &zero, MaxMissingRequiredRate: &zero, MaxBudgetOverrunRate: &zero}}
	r, err := e.GridSearch(testCtx, labels, g, "recall_at_5")
	if err != nil {
		t.Fatal(err)
	}
	if r.BestCondition != "grid/0002" || r.Trials[0].Eligible || r.Trials[0].Metrics.Recall5 != 1 || r.Trials[0].Metrics.ForbiddenHitRate != 1 {
		t.Fatal(r)
	}
	g.Weights["lexical"] = []float64{1}
	r, err = e.GridSearch(testCtx, labels, g, "recall_at_5")
	if err != nil || r.BestCondition != "" || r.Status != "no_feasible_configuration" {
		t.Fatal(r, err)
	}
	s := evaluationStats{}
	s.add([]string{"action"}, Label{Query: Query{Requires: map[string][]string{"action": {"check"}}}})
	s.addBudget(Result{TotalCost: 2}, Label{Query: Query{Budget: &Budget{Max: 1}}}, Budget{})
	if s.metrics().MissingRequiredRate != 1 || s.metrics().BudgetOverrunRate != 1 {
		t.Fatal(s.metrics())
	}
}
