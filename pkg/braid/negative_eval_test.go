package braid

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNegativeEvaluationAndAbstention(t *testing.T) {
	e := openTest(t, DefaultConfig())
	mustUpsert(t, e, Batch{Nodes: []Node{node("good", "task", "needle", 1), node("stale", "task", "obsolete", 1), node("other", "task", "unrelated", 1)}})
	labels := []Label{
		{QueryID: "positive", Query: Query{Text: "needle", RequireEvidence: true, At: testTime}, Expected: []string{"good"}},
		{QueryID: "negative", Query: Query{Text: "absenttoken", RequireEvidence: true, At: testTime}, NoAnswer: true, StaleIDs: []string{"stale"}, CrossWorkstreamIDs: []string{"other"}},
	}
	runs, err := e.Eval(testCtx, labels)
	if err != nil {
		t.Fatal(err)
	}
	full := runs[len(runs)-1].Metrics
	if full.AnswerableQueries != 1 || full.NoAnswerQueries != 1 || full.Recall5 != 1 || full.Coverage != .5 || full.AbstentionRate != .5 || full.NoAnswerFalsePositiveRate != 0 || full.Precision5 != 1 {
		t.Fatal(full)
	}
	temporal := runs[3].Metrics
	if temporal.NoAnswerFalsePositiveRate != 1 || temporal.StaleHitRate5 != 1 || temporal.CrossWorkstreamHitRate5 != 1 {
		t.Fatal(temporal)
	}
	grid, err := e.GridSearch(testCtx, labels, Grid{MMRLambda: []float64{.5, .7}}, "recall_at_5")
	if err != nil {
		t.Fatal(err)
	}
	for _, trial := range grid.Trials {
		if trial.Metrics.NoAnswerQueries != 1 || trial.Metrics.NoAnswerFalsePositiveRate != 0 {
			t.Fatal(trial)
		}
	}
	var saved string
	if err = e.db.QueryRow(`SELECT label FROM evaluation_labels WHERE query_id='negative'`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	var l Label
	if err = json.Unmarshal([]byte(saved), &l); err != nil || !l.NoAnswer || len(l.StaleIDs) != 1 {
		t.Fatal(saved, err)
	}
	r := mustQuery(t, e, labels[1].Query)
	if !r.Abstained || r.AbstentionReason != "evidence_policy" {
		t.Fatal(r)
	}
	threshold := 100.
	r = mustQuery(t, e, Query{Text: "needle", MinScore: &threshold})
	if !r.Abstained {
		t.Fatal("minimum score ignored")
	}
	_, err = e.Eval(testCtx, []Label{{QueryID: "invalid"}})
	if err == nil {
		t.Fatal("missing ground truth treated as no-answer")
	}
	_, err = e.Eval(testCtx, []Label{{QueryID: "invalid", NoAnswer: true, Expected: []string{"good"}}})
	if err == nil {
		t.Fatal("contradictory answerability accepted")
	}
}
func TestNoAnswerOnlyAndStatsDenominators(t *testing.T) {
	e := openTest(t, DefaultConfig())
	runs, err := e.Eval(testCtx, []Label{{QueryID: "none", NoAnswer: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Metrics.AbstentionRate != 1 || r.Metrics.AnswerableQueries != 0 || math.IsNaN(r.Metrics.Recall5) {
			t.Fatal(r)
		}
	}
	s := evaluationStats{}
	s.add([]string{"good", "stale", "other"}, Label{Expected: []string{"good"}, StaleIDs: []string{"stale"}, CrossWorkstreamIDs: []string{"other"}})
	s.add(nil, Label{NoAnswer: true})
	m := s.metrics()
	if m.Precision5 != 1.0/3 || m.StaleHitRate5 != 1 || m.CrossWorkstreamHitRate5 != 1 || m.Coverage != .5 {
		t.Fatal(m)
	}
}
