package braid

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

func eligible(n Node, q Query) bool {
	return (len(q.Filters.Types) == 0 || contains(q.Filters.Types, n.Type)) && (q.Filters.Since.IsZero() || !n.TS.Before(q.Filters.Since)) && !contains(q.Filters.ExcludeIDs, n.ID)
}
func rank(h []Hit, limit int) []Hit {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Score == h[j].Score {
			return h[i].ID < h[j].ID
		}
		return h[i].Score > h[j].Score
	})
	if len(h) > limit {
		h = h[:limit]
	}
	for i := range h {
		h[i].Rank = i + 1
	}
	return h
}
func (e *Engine) lexical(ctx context.Context, q Query, limit int) ([]Hit, error) {
	// Treat input as literal search terms, never as an FTS expression.
	terms := strings.FieldsFunc(q.Text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' })
	if len(terms) == 0 {
		return []Hit{}, nil
	}
	quoted := []string{}
	for _, t := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	s := `SELECT n.id,-bm25(nodes_fts),highlight(nodes_fts,0,'[',']') FROM nodes_fts JOIN nodes n ON n.rowid=nodes_fts.rowid WHERE nodes_fts MATCH ?`
	args := []any{strings.Join(quoted, " OR ")}
	if len(q.Filters.Types) > 0 {
		s += ` AND n.type IN (` + strings.TrimRight(strings.Repeat("?,", len(q.Filters.Types)), ",") + ")"
		for _, t := range q.Filters.Types {
			args = append(args, t)
		}
	}
	if !q.Filters.Since.IsZero() {
		s += ` AND n.ts>=?`
		args = append(args, timestamp(q.Filters.Since))
	}
	if len(q.Filters.ExcludeIDs) > 0 {
		s += ` AND n.id NOT IN (` + strings.TrimRight(strings.Repeat("?,", len(q.Filters.ExcludeIDs)), ",") + ")"
		for _, id := range q.Filters.ExcludeIDs {
			args = append(args, id)
		}
	}
	s += ` ORDER BY bm25(nodes_fts),n.id LIMIT ?`
	args = append(args, limit)
	rows, err := e.reader().QueryContext(ctx, s, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		var evidence string
		if err = rows.Scan(&h.ID, &h.Score, &evidence); err != nil {
			return nil, err
		}
		h.Rank = len(out) + 1
		h.Why = []string{fmt.Sprintf("lexical: rank %d matched %s", h.Rank, evidence)}
		out = append(out, h)
	}
	return out, rows.Err()
}
func (e *Engine) dense(ctx context.Context, q Query, nodes []Node, vs map[string][]float32, limit int) ([]Hit, error) {
	if e.cfg.Embed.Provider == "none" && e.provider == nil {
		return []Hit{}, nil
	}
	var v []float32
	if strings.TrimSpace(q.Text) != "" {
		if e.provider == nil {
			return nil, fmt.Errorf("text dense retrieval requires a provider")
		}
		out, err := e.provider.Embed(ctx, []string{q.Text})
		if err != nil {
			return nil, err
		}
		if err = validateVectors(out, 1); err != nil {
			return nil, err
		}
		v = out[0]
	} else {
		seen := map[string]bool{}
		count := 0
		for _, id := range q.Anchors {
			if seen[id] {
				continue
			}
			seen[id] = true
			if a, ok := vs[id]; ok {
				if v == nil {
					v = make([]float32, len(a))
				}
				for i, x := range a {
					v[i] += x
				}
				count++
			}
		}
		if count == 0 {
			return []Hit{}, nil
		}
		for i := range v {
			v[i] /= float32(count)
		}
	}
	if cosine(v, v) == 0 {
		return []Hit{}, nil
	}
	out := []Hit{}
	for _, n := range nodes {
		if !eligible(n, q) {
			continue
		}
		a, ok := vs[n.ID]
		if !ok {
			continue
		}
		if len(a) != len(v) {
			return nil, fmt.Errorf("query dimension %d differs from stored model dimension %d; reindex required", len(v), len(a))
		}
		out = append(out, Hit{ID: n.ID, Score: cosine(v, a)})
	}
	out = rank(out, limit)
	for i := range out {
		out[i].Why = []string{fmt.Sprintf("dense: rank %d cosine %.6f (model %s)", out[i].Rank, out[i].Score, e.cfg.Embed.Model)}
	}
	return out, nil
}

type neighbor struct {
	id     string
	weight float64
}
type neighborhood struct {
	adj   map[string][]neighbor
	ids   []string
	dist  map[string]int
	paths map[string][]string
}

func (e *Engine) neighborhood(ctx context.Context, q Query, nodes []Node) (neighborhood, error) {
	g := neighborhood{adj: map[string][]neighbor{}, dist: map[string]int{}, paths: map[string][]string{}}
	exists := map[string]bool{}
	for _, n := range nodes {
		exists[n.ID] = true
	}
	rows, err := e.reader().QueryContext(ctx, `SELECT src,dst,type,weight FROM edges ORDER BY src,dst,type`)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var src, dst, typ string
		var w float64
		if err = rows.Scan(&src, &dst, &typ, &w); err != nil {
			rows.Close()
			return g, err
		}
		if w <= 0 || !exists[src] || !exists[dst] {
			continue
		}
		g.adj[src] = append(g.adj[src], neighbor{dst, w})
		if !contains(e.cfg.Graph.Directed, typ) && src != dst {
			g.adj[dst] = append(g.adj[dst], neighbor{src, w})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, err
	}
	for id := range g.adj {
		sort.SliceStable(g.adj[id], func(i, j int) bool { return g.adj[id][i].id < g.adj[id][j].id })
	}
	anchors := append([]string{}, q.Anchors...)
	sort.Strings(anchors)
	for _, id := range anchors {
		if !exists[id] {
			continue
		}
		if _, ok := g.dist[id]; ok {
			continue
		}
		g.dist[id] = 0
		g.paths[id] = []string{id}
		g.ids = append(g.ids, id)
	}
	for head := 0; head < len(g.ids); head++ {
		id := g.ids[head]
		if g.dist[id] >= e.cfg.Graph.MaxHops {
			continue
		}
		for _, nb := range g.adj[id] {
			if _, ok := g.dist[nb.id]; ok {
				continue
			}
			g.dist[nb.id] = g.dist[id] + 1
			g.paths[nb.id] = append(append([]string{}, g.paths[id]...), nb.id)
			g.ids = append(g.ids, nb.id)
		}
	}
	sort.Strings(g.ids)
	return g, nil
}
func (e *Engine) graph(q Query, nodes []Node, g neighborhood, limit int) []Hit {
	if len(g.ids) == 0 {
		return []Hit{}
	}
	seed := map[string]float64{}
	sum := 0.
	for _, id := range g.ids {
		if g.dist[id] != 0 {
			continue
		}
		v := 1.
		if e.cfg.Graph.Seed == "inverse_degree" {
			unique := map[string]bool{}
			for _, n := range g.adj[id] {
				unique[n.id] = true
			}
			v /= float64(max(1, len(unique)))
		}
		seed[id] = v
		sum += v
	}
	for _, id := range g.ids {
		seed[id] /= sum
	}
	mass := map[string]float64{}
	for _, id := range g.ids {
		mass[id] = seed[id]
	}
	d := e.cfg.Graph.Damping
	for iter := 0; iter < 20; iter++ {
		next := map[string]float64{}
		dangling := 0.
		for _, id := range g.ids {
			next[id] = (1 - d) * seed[id]
		}
		for _, id := range g.ids {
			total := 0.
			for _, n := range g.adj[id] {
				if _, ok := g.dist[n.id]; ok {
					total += n.weight
				}
			}
			if total == 0 {
				dangling += mass[id]
				continue
			}
			for _, n := range g.adj[id] {
				if _, ok := g.dist[n.id]; ok {
					next[n.id] += d * mass[id] * n.weight / total
				}
			}
		}
		for _, id := range g.ids {
			next[id] += d * dangling * seed[id]
		}
		mass = next
	}
	out := []Hit{}
	for _, n := range nodes {
		if !eligible(n, q) || mass[n.ID] <= 0 {
			continue
		}
		out = append(out, Hit{ID: n.ID, Score: mass[n.ID], Why: []string{fmt.Sprintf("graph: %d hops via %s (mass %.6f)", g.dist[n.ID], strings.Join(g.paths[n.ID], " -> "), mass[n.ID])}})
	}
	return rank(out, limit)
}
func (e *Engine) temporal(q Query, nodes []Node, g neighborhood, limit int) []Hit {
	out := []Hit{}
	for _, n := range nodes {
		if !eligible(n, q) {
			continue
		}
		if e.cfg.Temporal.Neighborhood && len(q.Anchors) > 0 {
			if _, ok := g.dist[n.ID]; !ok {
				continue
			}
		}
		age := math.Max(0, q.At.Sub(n.TS).Hours())
		score := math.Exp(-math.Ln2 * age / e.cfg.Temporal.HalfLifeHours)
		out = append(out, Hit{ID: n.ID, Score: score, Why: []string{fmt.Sprintf("temporal: age %.3f hours (half-life %.3f hours, score %.6f)", age, e.cfg.Temporal.HalfLifeHours, score)}})
	}
	return rank(out, limit)
}
