package braid

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type Label struct {
	NoAnswer           bool     `json:"no_answer,omitempty"`
	StaleIDs           []string `json:"stale_ids,omitempty"`
	CrossWorkstreamIDs []string `json:"cross_workstream_ids,omitempty"`
	QueryID            string   `json:"query_id"`
	Query              Query    `json:"query"`
	Expected           []string `json:"expected"`
}
type Metrics struct {
	AnswerableQueries         int     `json:"answerable_queries"`
	NoAnswerQueries           int     `json:"no_answer_queries"`
	Coverage                  float64 `json:"coverage"`
	AbstentionRate            float64 `json:"abstention_rate"`
	NoAnswerFalsePositiveRate float64 `json:"no_answer_false_positive_rate"`
	Precision5                float64 `json:"precision_at_5"`
	StaleCases                int     `json:"stale_cases"`
	StaleHitRate5             float64 `json:"stale_hit_rate_at_5"`
	CrossWorkstreamCases      int     `json:"cross_workstream_cases"`
	CrossWorkstreamHitRate5   float64 `json:"cross_workstream_hit_rate_at_5"`
	Recall5                   float64 `json:"recall_at_5"`
	Recall20                  float64 `json:"recall_at_20"`
	MRR                       float64 `json:"mrr"`
	NDCG10                    float64 `json:"ndcg_at_10"`
}
type Evaluation struct {
	Dataset    DatasetState `json:"dataset"`
	Condition  string       `json:"condition"`
	Queries    int          `json:"queries"`
	Metrics    Metrics      `json:"metrics"`
	ConfigHash string       `json:"config_hash"`
}

func measure(ids, expected []string) Metrics {
	want := map[string]bool{}
	for _, id := range expected {
		want[id] = true
	}
	var m Metrics
	if len(want) == 0 {
		return m
	}
	seen := map[string]bool{}
	for i, id := range ids {
		if !want[id] || seen[id] {
			continue
		}
		seen[id] = true
		if i < 5 {
			m.Recall5++
		}
		if i < 20 {
			m.Recall20++
		}
		if m.MRR == 0 {
			m.MRR = 1 / float64(i+1)
		}
		if i < 10 {
			m.NDCG10 += 1 / math.Log2(float64(i+2))
		}
	}
	m.Recall5 /= float64(len(want))
	m.Recall20 /= float64(len(want))
	ideal := 0.
	for i := 0; i < min(10, len(want)); i++ {
		ideal += 1 / math.Log2(float64(i+2))
	}
	m.NDCG10 /= ideal
	return m
}

// Eval reports independent retrieval baselines and fused, budgeted contexts.
// A label's At is used if supplied; otherwise all conditions share one clock.
func (e *Engine) Eval(ctx context.Context, labels []Label) ([]Evaluation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	at := time.Now().UTC()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	view := &Engine{db: e.db, cfg: e.cfg, provider: e.provider, cost: e.cost, read: tx}
	runs, err := view.evaluate(ctx, labels, at)
	if err != nil {
		return nil, err
	}
	if err = tx.Rollback(); err != nil {
		return nil, err
	}
	return runs, e.persistEvaluation(ctx, labels, runs, at)
}

func (e *Engine) evaluate(ctx context.Context, labels []Label, at time.Time) ([]Evaluation, error) {
	state, err := e.dataset(ctx)
	if err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("eval needs labels")
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if l.QueryID == "" || seen[l.QueryID] || (len(l.Expected) == 0 && !l.NoAnswer) || (len(l.Expected) > 0 && l.NoAnswer) {
			return nil, fmt.Errorf("labels require unique query_id and expected IDs or explicit no_answer")
		}
		for _, id := range l.Expected {
			if id == "" || contains(l.StaleIDs, id) || contains(l.CrossWorkstreamIDs, id) {
				return nil, fmt.Errorf("label %s has empty or contradictory expected IDs", l.QueryID)
			}
		}
		seen[l.QueryID] = true
	}
	out := []Evaluation{}
	for _, name := range append(append([]string{}, retrieverNames...), "plain_rrf", "weighted_rrf", "braid") {
		condition := &Engine{db: e.db, cfg: e.cfg, provider: e.provider, cost: e.cost, read: e.read}
		if name == "plain_rrf" {
			condition.cfg.Defaults.K = map[string]float64{"lexical": 60, "dense": 60, "graph": 60, "temporal": 60}
			condition.cfg.Defaults.Weights = map[string]float64{"lexical": 1, "dense": 1, "graph": 1, "temporal": 1}
		}
		if name != "braid" {
			condition.cfg.Defaults.Boosts = nil
		}
		run := Evaluation{Condition: name, Queries: len(labels), ConfigHash: configHash(condition.cfg), Dataset: state}
		stats := evaluationStats{}
		for _, l := range labels {
			q := l.Query
			if q.At.IsZero() {
				q.At = at
			}
			ids := []string{}
			if contains(retrieverNames, name) {
				r := engineRetriever{e: e, name: name}
				hits, err := r.retrieve(ctx, q, max(20, e.cfg.CandidateLimit))
				if err != nil {
					return nil, err
				}
				for _, h := range hits {
					ids = append(ids, h.ID)
				}
			} else {
				q.Explain = true
				if name == "plain_rrf" {
					q.Weights = map[string]float64{"lexical": 1, "dense": 1, "graph": 1, "temporal": 1}
				}
				if name != "braid" {
					q.Boosts = nil
				}
				res, err := condition.query(ctx, q)
				if err != nil {
					return nil, err
				}
				if name == "braid" {
					for _, it := range res.Items {
						ids = append(ids, it.ID)
					}
				} else {
					for _, h := range res.FusedOrder {
						ids = append(ids, h.ID)
					}
				}
			}
			stats.add(ids, l)
		}
		run.Metrics = stats.metrics()
		out = append(out, run)
	}
	return out, nil
}

func (e *Engine) persistEvaluation(ctx context.Context, labels []Label, runs []Evaluation, at time.Time) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, l := range labels {
		if l.Query.At.IsZero() {
			l.Query.At = at
		}
		q, _ := json.Marshal(l.Query)
		ex, _ := json.Marshal(l.Expected)
		if _, err = tx.ExecContext(ctx, `INSERT INTO labels VALUES(?,?,?) ON CONFLICT(query_id) DO UPDATE SET query=excluded.query,expected=excluded.expected`, l.QueryID, string(q), string(ex)); err != nil {
			return err
		}
		full, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evaluation_labels(query_id,label) VALUES(?,?) ON CONFLICT(query_id) DO UPDATE SET label=excluded.label`, l.QueryID, string(full)); err != nil {
			return err
		}
	}
	for _, run := range runs {
		b, _ := json.Marshal(struct {
			Metrics
			Dataset DatasetState `json:"dataset"`
		}{run.Metrics, run.Dataset})
		if _, err = tx.ExecContext(ctx, `INSERT INTO eval_runs(ts,config_hash,condition,metrics) VALUES(?,?,?,?)`, timestamp(at), run.ConfigHash, run.Condition, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
