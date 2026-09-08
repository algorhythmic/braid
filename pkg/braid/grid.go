package braid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Grid contains Cartesian axes; omitted axes retain the base configuration.
type Grid struct {
	Constraints *EvalConstraints     `yaml:"constraints" json:"constraints,omitempty"`
	Weights     map[string][]float64 `yaml:"weights" json:"weights,omitempty"`
	K           map[string][]float64 `yaml:"k" json:"k,omitempty"`
	MMRLambda   []float64            `yaml:"mmr_lambda" json:"mmr_lambda,omitempty"`
	Damping     []float64            `yaml:"graph.damping" json:"graph.damping,omitempty"`
	Seed        []string             `yaml:"graph.seed" json:"graph.seed,omitempty"`
}
type GridTrial struct {
	Eligible   bool     `json:"eligible"`
	Violations []string `json:"violations,omitempty"`
	Evaluation
	Config Config `json:"config"`
}
type GridReport struct {
	Constraints   *EvalConstraints `json:"constraints,omitempty"`
	Status        string           `json:"status"`
	Dataset       DatasetState     `json:"dataset"`
	At            time.Time        `json:"at"`
	LabelsHash    string           `json:"labels_hash"`
	Objective     string           `json:"objective"`
	Baselines     []Evaluation     `json:"baselines"`
	Trials        []GridTrial      `json:"trials"`
	BestCondition string           `json:"best_condition"`
	Proposal      Config           `json:"proposal"`
}

func LoadGrid(path string) (Grid, error) {
	var g Grid
	b, err := os.ReadFile(path)
	if err != nil {
		return g, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err = d.Decode(&g); err != nil {
		return g, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return g, fmt.Errorf("grid must contain exactly one YAML document")
	}
	return g, nil
}
func cloneConfig(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}
func (g Grid) configurations(base Config) ([]Config, error) {
	if g.Constraints != nil {
		if err := g.Constraints.validate(); err != nil {
			return nil, err
		}
	}
	configs := []Config{cloneConfig(base)}
	axes := 0
	expand := func(n int, apply func(*Config, int)) error {
		if n == 0 {
			return fmt.Errorf("grid axes must not be empty")
		}
		axes++
		if n > 10000/len(configs) {
			return fmt.Errorf("grid exceeds 10000 combinations")
		}
		next := make([]Config, 0, len(configs)*n)
		for _, c := range configs {
			for i := 0; i < n; i++ {
				v := cloneConfig(c)
				apply(&v, i)
				if err := v.Validate(); err != nil {
					return fmt.Errorf("invalid grid value: %w", err)
				}
				next = append(next, v)
			}
		}
		configs = next
		return nil
	}
	for _, m := range []map[string][]float64{g.Weights, g.K} {
		for key := range m {
			if !contains(retrieverNames, key) {
				return nil, fmt.Errorf("unknown grid retriever %q", key)
			}
		}
	}
	for _, name := range retrieverNames {
		if values, ok := g.Weights[name]; ok {
			if err := expand(len(values), func(c *Config, i int) { c.Defaults.Weights[name] = values[i] }); err != nil {
				return nil, err
			}
		}
		if values, ok := g.K[name]; ok {
			if err := expand(len(values), func(c *Config, i int) { c.Defaults.K[name] = values[i] }); err != nil {
				return nil, err
			}
		}
	}
	if g.MMRLambda != nil {
		if err := expand(len(g.MMRLambda), func(c *Config, i int) { c.Defaults.MMRLambda = g.MMRLambda[i] }); err != nil {
			return nil, err
		}
	}
	if g.Damping != nil {
		if err := expand(len(g.Damping), func(c *Config, i int) { c.Graph.Damping = g.Damping[i] }); err != nil {
			return nil, err
		}
	}
	if g.Seed != nil {
		if err := expand(len(g.Seed), func(c *Config, i int) { c.Graph.Seed = g.Seed[i] }); err != nil {
			return nil, err
		}
	}
	if axes == 0 {
		return nil, fmt.Errorf("grid requires at least one axis")
	}
	return configs, nil
}
func metricValue(m Metrics, objective string) (float64, error) {
	switch objective {
	case "ndcg_at_10":
		return m.NDCG10, nil
	case "recall_at_5":
		return m.Recall5, nil
	case "recall_at_20":
		return m.Recall20, nil
	case "mrr":
		return m.MRR, nil
	default:
		return 0, fmt.Errorf("unknown grid objective %q", objective)
	}
}

// GridSearch evaluates full packed output for every combination. It never changes
// the active configuration. Ties select the first combination in stable axis order.
// Label weights are rejected so they cannot silently override the search axes.
func (e *Engine) GridSearch(ctx context.Context, labels []Label, g Grid, objective string) (GridReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	report := GridReport{Constraints: g.Constraints, Status: "no_feasible_configuration", At: time.Now().UTC(), Objective: objective}
	if _, err := metricValue(Metrics{}, objective); err != nil {
		return report, err
	}
	configs, err := g.configurations(e.cfg)
	if err != nil {
		return report, err
	}
	pinned := make([]Label, len(labels))
	copy(pinned, labels)
	answerable := 0
	for _, l := range pinned {
		if !l.NoAnswer && len(l.Expected) > 0 {
			answerable++
		}
	}
	if answerable == 0 {
		return report, fmt.Errorf("ranking grid objectives require at least one answerable label; use eval for no-answer-only sets")
	}
	for i := range pinned {
		if len(pinned[i].Query.Weights) > 0 {
			return report, fmt.Errorf("grid label %q has query weights; move weights into the base config or grid", pinned[i].QueryID)
		}
		if pinned[i].Query.At.IsZero() {
			pinned[i].Query.At = report.At
		}
	}
	b, err := json.Marshal(pinned)
	if err != nil {
		return report, err
	}
	hash := sha256.Sum256(b)
	report.LabelsHash = hex.EncodeToString(hash[:])
	// Memoize provider inputs for this run so model calls do not multiply with
	// grid size and identical inputs use identical query vectors across conditions.
	var provider Provider = e.provider
	if provider != nil {
		provider = &gridProvider{source: provider, cache: map[string][][]float32{}}
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	report.Dataset, err = stateFrom(ctx, tx)
	if err != nil {
		return report, err
	}
	base := &Engine{db: e.db, cfg: e.cfg, provider: provider, cost: e.cost, read: tx}
	report.Baselines, err = base.evaluate(ctx, pinned, report.At)
	if err != nil {
		return report, err
	}
	runs := append([]Evaluation{}, report.Baselines...)
	best := -1.
	report.Trials = make([]GridTrial, 0, len(configs))
	for i, c := range configs {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		condition := &Engine{db: e.db, cfg: c, provider: provider, cost: e.cost, read: tx}
		run := Evaluation{RankingVersion: RankingVersion, Condition: fmt.Sprintf("grid/%04d", i+1), Queries: len(pinned), ConfigHash: configHash(c), Dataset: report.Dataset}
		stats := evaluationStats{}
		for _, l := range pinned {
			q := l.Query
			q.Explain = false
			res, er := condition.queryOutput(ctx, q, l.Output)
			if er != nil {
				return report, fmt.Errorf("%s query %s: %w", run.Condition, l.QueryID, er)
			}
			ids := make([]string, 0, len(res.Items))
			for _, it := range res.Items {
				ids = append(ids, it.ID)
			}
			stats.add(ids, l)
			stats.addBudget(res, l, c.Defaults.Budget)
			l.Query = q
			run.Outcomes = append(run.Outcomes, outcome(l, res))
		}
		run.Metrics = stats.metrics()
		violations := g.Constraints.violations(run.Metrics)
		report.Trials = append(report.Trials, GridTrial{Evaluation: run, Config: c, Eligible: len(violations) == 0, Violations: violations})
		runs = append(runs, run)
		score, _ := metricValue(run.Metrics, objective)
		if len(violations) == 0 && score > best {
			report.Status = "ok"
			best = score
			report.BestCondition = run.Condition
			report.Proposal = cloneConfig(c)
		}
	}
	if err = tx.Rollback(); err != nil {
		return report, err
	}
	return report, e.persistEvaluation(ctx, pinned, runs, report.At)
}

type gridProvider struct {
	source Provider
	cache  map[string][][]float32
}

func (p *gridProvider) Model() string { return p.source.Model() }
func (p *gridProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	b, _ := json.Marshal(texts)
	key := string(b)
	if v, ok := p.cache[key]; ok {
		return v, nil
	}
	v, err := p.source.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if err = validateVectors(v, len(texts)); err != nil {
		return nil, err
	}
	p.cache[key] = v
	return v, nil
}
