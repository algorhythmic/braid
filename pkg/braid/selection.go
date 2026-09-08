package braid

import (
	"context"
	"fmt"
	"sort"
)

const RankingVersion = "mmr-admitted-dependencies-v2"

type CostAccounting struct {
	Unit      string  `json:"unit"`
	Estimator string  `json:"estimator"`
	Fixed     float64 `json:"fixed"`
	Content   float64 `json:"content"`
	Required  float64 `json:"required"`
}
type Rejection struct {
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// dependencyClosure is iterative, deterministic, and treats cycles as bundles.
func dependencyClosure(ctx context.Context, roots []string, requires map[string][]string) ([]string, error) {
	seen := map[string]bool{}
	todo := append([]string{}, roots...)
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		todo = append(todo, requires[id]...)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (e *Engine) pack(ctx context.Context, q Query, candidates []Hit, byID map[string]Node, vs map[string][]float32, output *Output, result *Result) error {
	budget := e.cfg.Defaults.Budget
	if q.Budget != nil {
		budget = *q.Budget
	}
	unit, estimator := budget.Unit, budget.Estimator
	if unit == "" {
		unit = "cost_units"
	}
	if estimator == "" {
		estimator = "node-cost-or-text-runes-div4-v1"
		if e.cost != nil {
			estimator = "consumer-cost-function"
		} else if e.cfg.Index.CostField != "" {
			estimator = "node-cost-or-attrs-" + e.cfg.Index.CostField + "-or-text-runes-div4-v1"
		}
	}
	result.Accounting = CostAccounting{Unit: unit, Estimator: estimator, Fixed: budget.Fixed}
	failMandatory := func(reason string) {
		result.Items = []Item{}
		result.TotalCost = 0
		result.Accounting.Fixed = 0
		result.AbstentionReason = reason
		result.Rejections = append(result.Rejections, Rejection{Reason: reason})
	}
	if budget.Fixed > budget.Max {
		failMandatory("infeasible_budget")
		return nil
	}
	result.TotalCost = budget.Fixed
	// Fetch prerequisites by exact ID, independent of relevance and candidate caps.
	roots := append([]string{}, q.MandatoryIDs...)
	for _, h := range candidates {
		roots = append(roots, h.ID)
	}
	ids, err := dependencyClosure(ctx, roots, q.Requires)
	if err != nil {
		return err
	}
	missing := []string{}
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		f := q.Filters
		if f.AllowedIDs == nil {
			f.AllowedIDs = missing
		} else {
			f.AllowedIDs = []string{}
			for _, id := range missing {
				if contains(q.Filters.AllowedIDs, id) {
					f.AllowedIDs = append(f.AllowedIDs, id)
				}
			}
		}
		nodes, er := e.filteredNodes(ctx, f)
		if er != nil {
			return er
		}
		for _, n := range nodes {
			byID[n.ID] = n
		}
	}
	if e.cfg.Defaults.MMRLambda < 1 && e.cfg.Embed.Model != "" && (len(candidates) > 1 || len(q.MandatoryIDs) > 0) {
		extra := []string{}
		for _, id := range ids {
			if _, ok := vs[id]; !ok {
				if _, ok := byID[id]; ok {
					extra = append(extra, id)
				}
			}
		}
		if len(extra) > 0 {
			more, er := e.vectorIDs(ctx, extra)
			if er != nil {
				return er
			}
			for id, v := range more {
				vs[id] = v
			}
		}
	}
	costs := map[string]float64{}
	terms := map[string]map[string]bool{}
	hits := map[string]Hit{}
	for _, h := range candidates {
		hits[h.ID] = h
	}
	for _, id := range ids {
		if n, ok := byID[id]; ok {
			c, er := e.nodeCost(n)
			if er != nil {
				return er
			}
			if !finite(c) || c < 0 {
				return fmt.Errorf("invalid cost for %s", id)
			}
			costs[id] = c
			terms[id] = redundancyTerms(n.Text)
		}
	}
	admitted := map[string]bool{}
	maxSim := map[string]float64{}
	closures := map[string][]string{}
	for _, h := range candidates {
		closure, er := dependencyClosure(ctx, []string{h.ID}, q.Requires)
		if er != nil {
			return er
		}
		closures[h.ID] = closure
	}
	contextBytes := 2
	outputCosts := map[string]int{}
	bundle := func(rootIDs []string) ([]string, string, error) {
		var all []string
		if len(rootIDs) == 1 {
			all = closures[rootIDs[0]]
		}
		if all == nil {
			var er error
			all, er = dependencyClosure(ctx, rootIDs, q.Requires)
			if er != nil {
				return nil, "", er
			}
		}
		add := []string{}
		for _, id := range all {
			if _, ok := byID[id]; !ok {
				return nil, "required_context_unavailable", nil
			}
			if !admitted[id] {
				add = append(add, id)
			}
		}
		return add, "", nil
	}
	makeItems := func(add []string, root string) []Item {
		items := append([]Item{}, result.Items...)
		// Put the action before its sorted prerequisites, retaining request-independent order.
		order := append([]string{}, add...)
		sort.SliceStable(order, func(i, j int) bool { return order[i] == root && order[j] != root })
		for _, id := range order {
			h := hits[id]
			why := h.Why
			if len(why) == 0 {
				why = []string{"required context"}
			}
			items = append(items, Item{Node: byID[id], Score: h.Score, CostValue: costs[id], Why: why})
		}
		return items
	}
	fits := func(add []string, root string) (bool, error) {
		nextCost := result.TotalCost
		for _, id := range add {
			nextCost += costs[id]
			if !finite(nextCost) || nextCost > budget.Max {
				return false, nil
			}
		}
		if output != nil {
			size := contextBytes
			for i, id := range add {
				cost, ok := outputCosts[id]
				if !ok {
					h := hits[id]
					why := h.Why
					if len(why) == 0 {
						why = []string{"required context"}
					}
					b, er := renderContext([]Item{{Node: byID[id], Score: h.Score, CostValue: costs[id], Why: why}}, *output)
					if er != nil {
						return false, er
					}
					cost = len(b) - 2
					outputCosts[id] = cost
				}
				if len(result.Items)+i > 0 {
					size++
				}
				size += cost
			}
			return size <= output.MaxBytes, nil
		}
		return true, nil
	}
	admit := func(add []string, root string) {
		if output != nil {
			for i, id := range add {
				if len(result.Items)+i > 0 {
					contextBytes++
				}
				contextBytes += outputCosts[id]
			}
		}
		result.Items = makeItems(add, root)
		for _, id := range add {
			admitted[id] = true
			result.TotalCost += costs[id]
			if id == root {
				result.Accounting.Content += costs[id]
			} else {
				result.Accounting.Required += costs[id]
			}
		}
		if e.cfg.Defaults.MMRLambda < 1 {
			for _, id := range add {
				for _, other := range ids {
					if admitted[other] {
						continue
					}
					sim := 0.
					a, aok := vs[id]
					b, bok := vs[other]
					if aok && bok {
						sim = cosine(a, b)
					} else {
						sim = termOverlap(terms[id], terms[other])
					}
					if sim > maxSim[other] {
						maxSim[other] = sim
					}
				}
			}
		}
	}
	mandatory, reason, err := bundle(q.MandatoryIDs)
	if err != nil {
		return err
	}
	if reason != "" {
		failMandatory(reason)
		return nil
	}
	ok, err := fits(mandatory, "")
	if err != nil {
		return err
	}
	if !ok {
		failMandatory("infeasible_budget")
		return nil
	}
	admit(mandatory, "")
	peak := 1.
	if len(candidates) > 0 {
		peak = candidates[0].Score
	}
	used := map[string]bool{}
	selected := 0
	for selected < e.cfg.CandidateLimit {
		if err := ctx.Err(); err != nil {
			return err
		}
		best := -1
		bestScore := 0.
		var bestAdd []string
		for i, h := range candidates {
			if used[h.ID] || admitted[h.ID] {
				continue
			}
			add, reason, er := bundle([]string{h.ID})
			if er != nil {
				return er
			}
			if reason != "" {
				used[h.ID] = true
				result.Rejections = append(result.Rejections, Rejection{ID: h.ID, Reason: reason})
				continue
			}
			fitsBudget, er := fits(add, h.ID)
			if er != nil {
				return er
			}
			if !fitsBudget {
				used[h.ID] = true
				result.DroppedForBudget++
				result.Rejections = append(result.Rejections, Rejection{ID: h.ID, Reason: "budget"})
				continue
			}
			// Only admitted context contributes redundancy. Skipped bundles never do.
			similarity := 0.
			for _, id := range add {
				if maxSim[id] > similarity {
					similarity = maxSim[id]
				}
			}
			score := e.cfg.Defaults.MMRLambda*(h.Score/peak) - (1-e.cfg.Defaults.MMRLambda)*similarity
			if best < 0 || score > bestScore {
				best = i
				bestScore = score
				bestAdd = add
			}
		}
		if best < 0 {
			break
		}
		h := candidates[best]
		used[h.ID] = true
		selected++
		admit(bestAdd, h.ID)
	}
	if len(result.Items) == 0 && len(result.Rejections) > 0 && result.AbstentionReason == "" {
		result.AbstentionReason = result.Rejections[0].Reason
	}
	return ctx.Err()
}
