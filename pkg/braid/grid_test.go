package braid

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGridCartesianValidation(t *testing.T) {
	g := Grid{Weights: map[string][]float64{"lexical": {.6, 1, 1.2}, "dense": {.8, 1, 1.3}, "graph": {.9, 1, 1.2}, "temporal": {.5, 1, 1.5}}, K: map[string][]float64{"graph": {20, 60}}, Damping: []float64{.5, .85}, Seed: []string{"uniform", "inverse_degree"}}
	c := DefaultConfig()
	before := configHash(c)
	configs, err := g.configurations(c)
	if err != nil || len(configs) != 648 {
		t.Fatal(len(configs), err)
	}
	if before != configHash(c) {
		t.Fatal("mutated base config")
	}
	configs[0].Defaults.Weights["lexical"] = 999
	if configs[1].Defaults.Weights["lexical"] == 999 {
		t.Fatal("aliased trial configs")
	}
	for _, bad := range []Grid{{}, {Weights: map[string][]float64{"typo": {1}}}, {K: map[string][]float64{"graph": {}}}, {Damping: []float64{1}}, {Seed: []string{"random"}}, {MMRLambda: []float64{-1}}, {MMRLambda: make([]float64, 10001)}} {
		if _, err := bad.configurations(c); err == nil {
			t.Fatal("invalid grid accepted", bad)
		}
	}
}
func TestGridSelectsWinnerAndPreservesConfig(t *testing.T) {
	c := DefaultConfig()
	c.Defaults.Weights = weights("lexical")
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "task", "irrelevant", 1), node("z", "task", "needle", 1)}})
	labels := []Label{{QueryID: "q", Query: Query{Text: "needle", At: testTime}, Expected: []string{"z"}}}
	g := Grid{Weights: map[string][]float64{"lexical": {0, 1}}}
	before := configHash(e.cfg)
	r, err := e.GridSearch(testCtx, labels, g, "recall_at_5")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Baselines) != 7 || len(r.Trials) != 2 || r.BestCondition != "grid/0002" || r.Trials[0].Metrics.Recall5 != 0 || r.Trials[1].Metrics.Recall5 != 1 {
		t.Fatalf("unexpected report: %+v", r)
	}
	if before != configHash(e.cfg) || r.LabelsHash == "" {
		t.Fatal("config changed or missing label identity")
	}
	var count int
	if err = e.db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&count); err != nil || count != 9 {
		t.Fatal("runs not persisted", count, err)
	}
	again, err := e.GridSearch(testCtx, labels, g, "recall_at_5")
	if err != nil || again.LabelsHash != r.LabelsHash || !reflect.DeepEqual(again.Trials, r.Trials) {
		t.Fatal("pinned grid changed", err)
	}
	labels[0].Query.Weights = weights("graph")
	if _, err = e.GridSearch(testCtx, labels, g, "recall_at_5"); err == nil {
		t.Fatal("query weights silently override grid")
	}
}
func TestGridStrictYAML(t *testing.T) {
	for _, content := range []string{"graph.dampng: [0.5]\n", "graph.damping: [0.5]\n---\n{}"} {
		path := filepath.Join(t.TempDir(), "grid.yaml")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadGrid(path); err == nil {
			t.Fatal("invalid YAML accepted")
		}
	}
}

type countingProvider struct{ calls int }

func (p *countingProvider) Model() string { return "fixture-v1" }
func (p *countingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	p.calls++
	return fixtureProvider{}.Embed(ctx, texts)
}
func TestGridReusesProviderCalls(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	p := &countingProvider{}
	e := openTest(t, c, WithProvider(p))
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "task", "needle", 1)}})
	if err := e.Reindex(testCtx); err != nil {
		t.Fatal(err)
	}
	p.calls = 0
	_, err := e.GridSearch(testCtx, []Label{{QueryID: "q", Query: Query{Text: "needle", At: testTime}, Expected: []string{"a"}}}, Grid{MMRLambda: []float64{0, .7, 1}}, "mrr")
	if err != nil || p.calls != 1 {
		t.Fatal("provider calls multiplied across conditions", p.calls, err)
	}
}
