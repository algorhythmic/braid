package braid

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func (e *Engine) validateQuery(q Query) error {
	if q.MinScore != nil && (!finite(*q.MinScore) || *q.MinScore < 0) {
		return fmt.Errorf("min_score must be finite and nonnegative")
	}
	if q.DatasetID != "" && q.DatasetID != e.cfg.DatasetID {
		return ErrDatasetMismatch
	}
	for k, v := range q.Weights {
		if !contains(retrieverNames, k) || !finite(v) || v < 0 {
			return fmt.Errorf("invalid query weight %q", k)
		}
	}
	if q.Budget != nil && (!finite(q.Budget.Max) || q.Budget.Max < 0) {
		return fmt.Errorf("budget must be finite and nonnegative")
	}
	for _, b := range q.Boosts {
		if err := validateBoost(b); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) nodeCost(n Node) (float64, error) {
	var v float64
	if e.cost != nil {
		return e.cost(n)
	}
	if n.Cost != nil {
		return *n.Cost, nil
	}
	if e.cfg.Index.CostField != "" {
		if x, ok := field(n.Attrs, e.cfg.Index.CostField); ok {
			var valid bool
			v, valid = x.(float64)
			if !valid {
				return 0, fmt.Errorf("cost field on %s is not numeric", n.ID)
			}
			return v, nil
		}
	}
	return float64(max(1, (utf8.RuneCountInString(n.Text)+3)/4)), nil
}
func boostMatches(b Boost, n Node) bool {
	if b.Type != "" && b.Type != n.Type {
		return false
	}
	if b.Where == "" {
		return true
	}
	p := strings.SplitN(b.Where, "==", 2)
	key := strings.TrimPrefix(strings.TrimSpace(p[0]), "attrs.")
	want := strings.Trim(strings.TrimSpace(p[1]), `"'`)
	v, ok := field(n.Attrs, key)
	return ok && fmt.Sprint(v) == want
}

// Query runs independent retrievers, weighted RRF, an MMR shortlist, and packing.
func (e *Engine) Query(ctx context.Context, q Query) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.query(ctx, q)
}
func (e *Engine) query(ctx context.Context, q Query) (Result, error) {
	if e.read == nil {
		tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return Result{}, err
		}
		defer tx.Rollback()
		view := &Engine{db: e.db, cfg: e.cfg, provider: e.provider, cost: e.cost, read: tx}
		return view.query(ctx, q)
	}
	result := Result{Items: []Item{}, ConfigHash: configHash(e.cfg)}
	if err := e.validateQuery(q); err != nil {
		return result, err
	}
	if q.At.IsZero() {
		q.At = time.Now().UTC()
	}
	state, err := e.dataset(ctx)
	if err != nil {
		return result, err
	}
	result.Dataset = state
	nodes, err := e.nodes(ctx)
	if err != nil {
		return result, err
	}
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	vs, err := e.vectors(ctx)
	if err != nil {
		return result, err
	}
	g, err := e.neighborhood(ctx, q, nodes)
	if err != nil {
		return result, err
	}
	weights := map[string]float64{}
	for _, r := range retrieverNames {
		weights[r] = e.cfg.Defaults.Weights[r]
		if w, ok := q.Weights[r]; ok {
			weights[r] = w
		}
	}
	lists := map[string][]Hit{}
	for _, r := range retrieverNames {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		lists[r] = []Hit{}
		if weights[r] == 0 {
			continue
		}
		switch r {
		case "lexical":
			lists[r], err = e.lexical(ctx, q, e.cfg.CandidateLimit)
		case "dense":
			lists[r], err = e.dense(ctx, q, nodes, vs, e.cfg.CandidateLimit)
		case "graph":
			lists[r] = e.graph(q, nodes, g, e.cfg.CandidateLimit)
		case "temporal":
			lists[r] = e.temporal(q, nodes, g, e.cfg.CandidateLimit)
		}
		if err != nil {
			return result, fmt.Errorf("%s: %w", r, err)
		}
	}
	fused := map[string]*Hit{}
	supported := map[string]bool{}
	for _, r := range retrieverNames {
		for _, h := range lists[r] {
			if r != "temporal" {
				supported[h.ID] = true
			}
			f := fused[h.ID]
			if f == nil {
				f = &Hit{ID: h.ID}
				fused[h.ID] = f
			}
			f.Score += weights[r] / (e.cfg.Defaults.K[r] + float64(h.Rank))
			f.Why = append(f.Why, h.Why...)
		}
	}
	boosts := append(append([]Boost{}, e.cfg.Defaults.Boosts...), q.Boosts...)
	candidates := []Hit{}
	for _, n := range nodes {
		h, ok := fused[n.ID]
		if !ok {
			continue
		}
		for _, b := range boosts {
			if boostMatches(b, n) {
				h.Score *= b.Factor
				h.Why = append(h.Why, fmt.Sprintf("boost: factor %.6f (%s %s)", b.Factor, b.Type, b.Where))
			}
		}
		if !finite(h.Score) {
			return result, fmt.Errorf("fused score overflow for %s", n.ID)
		}
		if h.Score > 0 {
			candidates = append(candidates, *h)
		}
	}
	candidates = rank(candidates, len(candidates))
	result.Considered = len(candidates)
	if q.Explain {
		result.Ranks = lists
		result.FusedOrder = append([]Hit{}, candidates...)
	}
	eligibleCandidates := make([]Hit, 0, len(candidates))
	for _, h := range candidates {
		if q.MinScore != nil && h.Score < *q.MinScore {
			continue
		}
		if q.RequireEvidence && !supported[h.ID] {
			continue
		}
		eligibleCandidates = append(eligibleCandidates, h)
	}
	if len(candidates) > 0 && len(eligibleCandidates) == 0 {
		result.AbstentionReason = "evidence_policy"
	}
	candidates = eligibleCandidates
	// MMR reduces the union to candidate_limit before packing. Merely reordering
	// every candidate here would be undone by the later value/cost sort.
	chosen := []Hit{}
	used := map[string]bool{}
	maxSim := map[string]float64{}
	textTerms := map[string]map[string]bool{}
	for _, h := range candidates {
		textTerms[h.ID] = redundancyTerms(byID[h.ID].Text)
	}
	peak := 1.0
	if len(candidates) > 0 {
		peak = candidates[0].Score
	}
	for len(chosen) < min(e.cfg.CandidateLimit, len(candidates)) {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		best := -1
		bestScore := 0.
		for i, h := range candidates {
			if used[h.ID] {
				continue
			}
			// Normalize only for MMR; preserve raw fused scores for reporting and packing.
			score := e.cfg.Defaults.MMRLambda*(h.Score/peak) - (1-e.cfg.Defaults.MMRLambda)*maxSim[h.ID]
			if best < 0 || score > bestScore {
				best = i
				bestScore = score
			}
		}
		h := candidates[best]
		used[h.ID] = true
		chosen = append(chosen, h)
		for _, other := range candidates {
			if used[other.ID] {
				continue
			}
			sim := 0.
			a, aok := vs[h.ID]
			b, bok := vs[other.ID]
			if aok && bok {
				sim = cosine(a, b)
			} else {
				sim = termOverlap(textTerms[h.ID], textTerms[other.ID])
			}
			if sim > maxSim[other.ID] {
				maxSim[other.ID] = sim
			}
		}
	}
	costs := map[string]float64{}
	order := map[string]int{}
	for i, h := range chosen {
		c, er := e.nodeCost(byID[h.ID])
		if er != nil {
			return result, er
		}
		if !finite(c) || c < 0 {
			return result, fmt.Errorf("invalid cost for %s", h.ID)
		}
		costs[h.ID] = c
		order[h.ID] = i
	}
	sort.SliceStable(chosen, func(i, j int) bool {
		a, b := chosen[i], chosen[j]
		ca, cb := costs[a.ID], costs[b.ID]
		if ca == 0 || cb == 0 {
			if ca == cb {
				return order[a.ID] < order[b.ID]
			}
			return ca == 0
		}
		ra, rb := a.Score/ca, b.Score/cb
		if ra == rb {
			return order[a.ID] < order[b.ID]
		}
		return ra > rb
	})
	budget := e.cfg.Defaults.Budget.Max
	if q.Budget != nil {
		budget = q.Budget.Max
	}
	for _, h := range chosen {
		c := costs[h.ID]
		if c > budget-result.TotalCost {
			result.DroppedForBudget++
			continue
		}
		result.Items = append(result.Items, Item{Node: byID[h.ID], CostValue: c, Score: h.Score, Why: h.Why})
		result.TotalCost += c
	}
	result.Abstained = len(result.Items) == 0
	if result.Abstained && result.AbstentionReason == "" {
		if len(candidates) == 0 {
			result.AbstentionReason = "no_candidates"
		} else {
			result.AbstentionReason = "budget"
		}
	}
	return result, nil
}

// Missing vectors fall back to lexical evidence, never domain type labels.
// Empty/punctuation-only text supplies no evidence of redundancy.
func redundancyTerms(text string) map[string]bool {
	out := map[string]bool{}
	for _, term := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' }) {
		out[term] = true
	}
	return out
}
func termOverlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	common := 0
	for term := range a {
		if b[term] {
			common++
		}
	}
	return float64(common) / float64(len(a)+len(b)-common)
}

type engineRetriever struct {
	e    *Engine
	name string
}

// Retriever exposes one raw ranked list, without fusion, boosts or packing.
func (e *Engine) Retriever(name string) (Retriever, error) {
	if !contains(retrieverNames, name) {
		return nil, fmt.Errorf("unknown retriever %q", name)
	}
	return engineRetriever{e, name}, nil
}
func (r engineRetriever) Retrieve(ctx context.Context, q Query, limit int) ([]Hit, error) {
	e := r.e
	e.mu.Lock()
	defer e.mu.Unlock()
	return r.retrieve(ctx, q, limit)
}
func (r engineRetriever) retrieve(ctx context.Context, q Query, limit int) ([]Hit, error) {
	e := r.e
	if e.read == nil {
		tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		view := &Engine{db: e.db, cfg: e.cfg, provider: e.provider, cost: e.cost, read: tx}
		return (engineRetriever{e: view, name: r.name}).retrieve(ctx, q, limit)
	}
	if limit <= 0 {
		return []Hit{}, nil
	}
	if err := e.validateQuery(q); err != nil {
		return nil, err
	}
	if q.At.IsZero() {
		q.At = time.Now().UTC()
	}
	if r.name == "lexical" {
		return e.lexical(ctx, q, limit)
	}
	nodes, err := e.nodes(ctx)
	if err != nil {
		return nil, err
	}
	if r.name == "dense" {
		vs, er := e.vectors(ctx)
		if er != nil {
			return nil, er
		}
		return e.dense(ctx, q, nodes, vs, limit)
	}
	g, err := e.neighborhood(ctx, q, nodes)
	if err != nil {
		return nil, err
	}
	if r.name == "graph" {
		return e.graph(q, nodes, g, limit), nil
	}
	return e.temporal(q, nodes, g, limit), nil
}
