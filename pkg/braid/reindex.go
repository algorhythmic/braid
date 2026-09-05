package braid

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
)

type ReindexOptions struct {
	Force bool `json:"force"`
}
type ReindexResult struct {
	Dataset   DatasetState `json:"dataset"`
	Reused    int          `json:"reused"`
	Generated int          `json:"generated"`
}

func (e *Engine) Reindex(ctx context.Context) error {
	_, err := e.ReindexWithOptions(ctx, ReindexOptions{})
	return err
}

// ReindexWithOptions builds in memory outside the engine lock, then publishes
// only if the captured dataset revision is still current. Unchanged vectors are
// reusable because every public text mutation invalidates its cached vectors.
func (e *Engine) ReindexWithOptions(ctx context.Context, opts ReindexOptions) (ReindexResult, error) {
	result := ReindexResult{}
	nodes, cached, state, err := e.reindexInput(ctx, opts)
	if err != nil {
		return result, err
	}
	batch := Batch{}
	missing := []Node{}
	dim := 0
	for _, n := range nodes {
		if v, ok := cached[n.ID]; ok {
			if dim != 0 && dim != len(v) {
				return result, fmt.Errorf("cached dimensions disagree; use force reindex")
			}
			dim = len(v)
			batch.Embeddings = append(batch.Embeddings, Embedding{NodeID: n.ID, Model: e.cfg.Embed.Model, Vec: v})
			result.Reused++
		} else {
			missing = append(missing, n)
		}
	}
	for start := 0; start < len(missing); start += 64 {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		end := min(start+64, len(missing))
		texts := []string{}
		for _, n := range missing[start:end] {
			texts = append(texts, n.Text)
		}
		vs, er := e.provider.Embed(ctx, texts)
		if er != nil {
			return result, er
		}
		if er = validateVectors(vs, len(texts)); er != nil {
			return result, er
		}
		for i, v := range vs {
			if dim != 0 && dim != len(v) {
				return result, fmt.Errorf("generated dimensions disagree with build; use a new model ID or force reindex")
			}
			dim = len(v)
			batch.Embeddings = append(batch.Embeddings, Embedding{NodeID: missing[start+i].ID, Model: e.cfg.Embed.Model, Vec: v})
			result.Generated++
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if e.provider.Model() != e.cfg.Embed.Model {
		return result, fmt.Errorf("provider model changed during reindex")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE dataset_state SET revision=revision WHERE singleton=1`); err != nil {
		return result, err
	}
	current, err := stateFrom(ctx, tx)
	if err != nil {
		return result, err
	}
	if current != state {
		return result, fmt.Errorf("%w: dataset changed during reindex", ErrRevisionConflict)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM embeddings`); err != nil {
		return result, err
	}
	if err = putBatch(ctx, tx, batch); err != nil {
		return result, err
	}
	result.Dataset, err = e.publish(ctx, tx)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (e *Engine) reindexInput(ctx context.Context, opts ReindexOptions) ([]Node, map[string][]float32, DatasetState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var state DatasetState
	if e.provider == nil {
		return nil, nil, state, fmt.Errorf("reindex requires an embedding provider")
	}
	if e.provider.Model() != e.cfg.Embed.Model {
		return nil, nil, state, fmt.Errorf("provider model does not match config")
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, state, err
	}
	defer tx.Rollback()
	state, err = stateFrom(ctx, tx)
	if err != nil {
		return nil, nil, state, err
	}
	view := &Engine{db: e.db, cfg: e.cfg, read: tx}
	nodes, err := view.nodes(ctx)
	if err != nil {
		return nil, nil, state, err
	}
	cache := map[string][]float32{}
	if opts.Force {
		return nodes, cache, state, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id,dim,vec FROM embeddings WHERE model=? ORDER BY node_id`, e.cfg.Embed.Model)
	if err != nil {
		return nil, nil, state, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d int
		var b []byte
		if err = rows.Scan(&id, &d, &b); err != nil {
			return nil, nil, state, err
		}
		if d <= 0 || d > len(b)/4 || len(b) != 4*d {
			continue
		}
		v := make([]float32, d)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		}
		if validateVectors([][]float32{v}, 1) == nil {
			cache[id] = v
		}
	}
	return nodes, cache, state, rows.Err()
}
