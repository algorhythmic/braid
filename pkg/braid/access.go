package braid

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// A zero-value mutex that can abandon waiting without leaking a goroutine.
type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextMutex) init() {
	m.once.Do(func() { m.token = make(chan struct{}, 1); m.token <- struct{}{} })
}
func (m *contextMutex) Lock() { _ = m.LockContext(context.Background()) }
func (m *contextMutex) LockContext(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}
func (m *contextMutex) Unlock() { m.token <- struct{}{} }

type GetRequest struct {
	DatasetID        string   `json:"dataset_id"`
	IDs              []string `json:"ids"`
	ExpectedRevision *int64   `json:"expected_revision,omitempty"`
}
type GetResult struct {
	Dataset    DatasetState `json:"dataset"`
	Nodes      []Node       `json:"nodes"`
	MissingIDs []string     `json:"missing_ids"`
}

func (e *Engine) readRevision(ctx context.Context, expected *int64) (DatasetState, error) {
	state, err := e.dataset(ctx)
	if err != nil {
		return state, err
	}
	if state.ID != e.cfg.DatasetID {
		return state, ErrDatasetMismatch
	}
	if expected != nil {
		if *expected < 0 {
			return state, fmt.Errorf("expected_revision must be nonnegative")
		}
		if state.Revision != *expected {
			return state, fmt.Errorf("%w: expected %d, current %d", ErrRevisionConflict, *expected, state.Revision)
		}
	}
	return state, nil
}

// GetMany returns unique IDs in request order from one current read snapshot.
// An explicit dataset binding is required. No historical revisions are retained.
func (e *Engine) GetMany(ctx context.Context, req GetRequest) (result GetResult, err error) {
	defer func() { err = contextError(ctx, err) }()
	result = GetResult{Nodes: []Node{}, MissingIDs: []string{}}
	if req.DatasetID == "" || req.DatasetID != e.cfg.DatasetID {
		return result, ErrDatasetMismatch
	}
	if req.IDs == nil {
		return result, fmt.Errorf("ids must be an explicit array")
	}
	if err := e.mu.LockContext(ctx); err != nil {
		return result, err
	}
	defer e.mu.Unlock()
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	view := &Engine{db: e.db, cfg: e.cfg, read: tx}
	result.Dataset, err = view.readRevision(ctx, req.ExpectedRevision)
	if err != nil {
		return result, err
	}
	nodes, err := view.filteredNodes(ctx, Filters{AllowedIDs: req.IDs})
	if err != nil {
		return result, err
	}
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	seen := map[string]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if n, ok := byID[id]; ok {
			result.Nodes = append(result.Nodes, n)
		} else {
			result.MissingIDs = append(result.MissingIDs, id)
		}
	}
	return result, ctx.Err()
}

type StageStats struct {
	Rows           int   `json:"rows"`
	DecodedBytes   int   `json:"decoded_bytes"`
	DurationMicros int64 `json:"duration_us"`
}

func (e *Engine) recordRead(stage string, bytes int) {
	if e.stages != nil {
		s := e.stages[stage]
		s.Rows++
		s.DecodedBytes += bytes
		e.stages[stage] = s
	}
}
func (e *Engine) recordTime(stage string, start time.Time) {
	if e.stages != nil {
		s := e.stages[stage]
		s.DurationMicros += time.Since(start).Microseconds()
		e.stages[stage] = s
	}
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
