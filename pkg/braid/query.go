package braid

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func (e *Engine) validateQuery(q Query) error {
	if _, _, err := filterSQL(q.Filters); err != nil {
		return err
	}
	if q.ExpectedRevision != nil && *q.ExpectedRevision < 0 {
		return fmt.Errorf("expected_revision must be nonnegative")
	}
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
	if q.Budget != nil && (!finite(q.Budget.Max) || q.Budget.Max < 0 || !finite(q.Budget.Fixed) || q.Budget.Fixed < 0) {
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

// Query runs independent retrievers, weighted RRF, and atomic budgeted MMR selection.
func (e *Engine) Query(ctx context.Context, q Query) (Result, error) {
	if err := e.mu.LockContext(ctx); err != nil {
		return Result{}, err
	}
	defer e.mu.Unlock()
	result, err := e.query(ctx, q)
	return result, contextError(ctx, err)
}
func (e *Engine) query(ctx context.Context, q Query) (Result, error) {
	return e.queryOutput(ctx, q, nil)
}
func (e *Engine) queryOutput(ctx context.Context, q Query, output *Output) (Result, error) {
	if e.read == nil {
		tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return Result{}, err
		}
		defer tx.Rollback()
		view := &Engine{db: e.db, cfg: e.cfg, provider: e.provider, cost: e.cost, read: tx}
		return view.queryOutput(ctx, q, output)
	}
	result := Result{Items: []Item{}, ConfigHash: configHash(e.cfg), RankingVersion: RankingVersion}
	e.stages = nil
	if q.Trace {
		e.stages = map[string]StageStats{}
		result.Stages = e.stages
	}
	if err := e.validateQuery(q); err != nil {
		return result, err
	}
	if q.At.IsZero() {
		q.At = time.Now().UTC()
	}
	state, err := e.readRevision(ctx, q.ExpectedRevision)
	if err != nil {
		return result, err
	}
	result.Dataset = state
	// Package A: https://github.com/algorhythmic/muninn/blob/8bdda8b/docs/braid-integration/A-EMBEDDING-COMPATIBILITY.md
	if err := e.validateQueryEmbedding(ctx, q); err != nil {
		return result, err
	}
	weights := map[string]float64{}
	for _, r := range retrieverNames {
		weights[r] = e.cfg.Defaults.Weights[r]
		if w, ok := q.Weights[r]; ok {
			weights[r] = w
		}
	}
	var nodes []Node
	byID := map[string]Node{}
	vs := map[string][]float32{}
	var g neighborhood
	if weights["dense"] > 0 || weights["graph"] > 0 || weights["temporal"] > 0 {
		nodes, err = e.filteredNodes(ctx, q.Filters)
		if err != nil {
			return result, err
		}
	}
	if weights["dense"] > 0 {
		vs, err = e.vectors(ctx)
		if err != nil {
			return result, err
		}
	}
	if len(q.Anchors) > 0 && (weights["graph"] > 0 || (weights["temporal"] > 0 && e.cfg.Temporal.Neighborhood)) {
		start := time.Now()
		g, err = e.neighborhood(ctx, q)
		e.recordTime("graph", start)
		if err != nil {
			return result, err
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
		start := time.Now()
		switch r {
		case "lexical":
			lists[r], err = e.lexical(ctx, q, e.cfg.CandidateLimit)
		case "dense":
			lists[r], err = e.dense(ctx, q, nodes, vs, e.cfg.CandidateLimit)
		case "graph":
			lists[r], err = e.graph(ctx, q, nodes, g, e.cfg.CandidateLimit)
		case "temporal":
			lists[r], err = e.temporal(ctx, q, nodes, g, e.cfg.CandidateLimit)
		}
		e.recordTime(r+"_retrieve", start)
		if err != nil {
			return result, fmt.Errorf("%s: %w", r, err)
		}
	}
	if weights["lexical"] > 0 {
		result.LexicalStatus = "matched"
		if len(lists["lexical"]) == 0 {
			result.LexicalStatus = "no_matches"
			where, _, _ := filterSQL(q.Filters)
			if where != "1" {
				probe := q
				probe.Filters = Filters{}
				hits, er := e.lexical(ctx, probe, 1)
				if er != nil {
					return result, er
				}
				if len(hits) > 0 {
					result.LexicalStatus = "filtered_out"
				}
			}
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
	if nodes == nil {
		ids := []string{}
		for id := range fused {
			ids = append(ids, id)
		}
		nodes, err = e.filteredNodes(ctx, Filters{AllowedIDs: ids})
		if err != nil {
			return result, err
		}
	}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	boosts := append(append([]Boost{}, e.cfg.Defaults.Boosts...), q.Boosts...)
	candidates := []Hit{}
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
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
	start := time.Now()
	if err = e.pack(ctx, q, candidates, byID, vs, output, &result); err != nil {
		return result, err
	}
	e.recordTime("selection", start)

	result.Abstained = len(result.Items) == 0
	if !result.Abstained {
		result.AbstentionReason = ""
	}
	if result.Abstained && result.AbstentionReason == "" {
		if len(candidates) == 0 {
			result.AbstentionReason = "no_candidates"
			if result.LexicalStatus == "filtered_out" {
				result.AbstentionReason = "eligibility"
			}
		} else {
			result.AbstentionReason = "budget"
		}
	}
	return result, ctx.Err()
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
	if err := e.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer e.mu.Unlock()
	hits, err := r.retrieve(ctx, q, limit)
	return hits, contextError(ctx, err)
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
	if err := e.validateQuery(q); err != nil {
		return nil, err
	}
	if _, err := e.readRevision(ctx, q.ExpectedRevision); err != nil {
		return nil, err
	}
	if err := e.validateQueryEmbedding(ctx, q); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []Hit{}, nil
	}
	if q.At.IsZero() {
		q.At = time.Now().UTC()
	}
	if r.name == "lexical" {
		return e.lexical(ctx, q, limit)
	}
	nodes, err := e.filteredNodes(ctx, q.Filters)
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
	var g neighborhood
	if len(q.Anchors) > 0 && (r.name == "graph" || e.cfg.Temporal.Neighborhood) {
		g, err = e.neighborhood(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	if r.name == "graph" {
		return e.graph(ctx, q, nodes, g, limit)
	}
	return e.temporal(ctx, q, nodes, g, limit)
}
