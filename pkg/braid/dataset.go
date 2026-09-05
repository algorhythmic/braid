package braid

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// DatasetState identifies the database's immutable scope and published content.
type DatasetState struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}
type Snapshot struct {
	DatasetID        string `json:"dataset_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	Batch            *Batch `json:"batch"`
}
type EdgeKey struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Type string `json:"type"`
}
type Update struct {
	DatasetID        string    `json:"dataset_id"`
	ExpectedRevision *int64    `json:"expected_revision"`
	Upsert           Batch     `json:"upsert"`
	DeleteNodes      []string  `json:"delete_nodes"`
	DeleteEdges      []EdgeKey `json:"delete_edges"`
}

var ErrRevisionConflict = errors.New("dataset revision conflict")
var ErrDatasetMismatch = errors.New("dataset does not match the bound dataset")

type sqlReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (e *Engine) reader() sqlReader {
	if e.read != nil {
		return e.read
	}
	return e.db
}
func stateFrom(ctx context.Context, r sqlReader) (DatasetState, error) {
	var s DatasetState
	err := r.QueryRowContext(ctx, `SELECT dataset_id,revision,digest FROM dataset_state WHERE singleton=1`).Scan(&s.ID, &s.Revision, &s.Digest)
	return s, err
}
func (e *Engine) dataset(ctx context.Context) (DatasetState, error) {
	return stateFrom(ctx, e.reader())
}
func (e *Engine) Dataset(ctx context.Context) (DatasetState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dataset(ctx)
}
func (e *Engine) initializeDataset(ctx context.Context) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS dataset_state(singleton INTEGER PRIMARY KEY CHECK(singleton=1),dataset_id TEXT NOT NULL,revision INTEGER NOT NULL,digest TEXT NOT NULL,mode TEXT NOT NULL DEFAULT 'legacy')`); err != nil {
		return err
	}
	s, err := stateFrom(ctx, tx)
	if err == sql.ErrNoRows {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM nodes)+(SELECT count(*) FROM edges)+(SELECT count(*) FROM events)+(SELECT count(*) FROM labels)+(SELECT count(*) FROM eval_runs)`).Scan(&count); err != nil {
			return err
		}
		if count > 0 && e.cfg.DatasetID != "default" {
			return fmt.Errorf("populated legacy database may only bind to default; rebuild a new named dataset")
		}
		digest, er := contentDigest(ctx, tx)
		if er != nil {
			return er
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dataset_state(singleton,dataset_id,revision,digest) VALUES(1,?,0,?)`, e.cfg.DatasetID, digest); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if s.ID != e.cfg.DatasetID {
		return ErrDatasetMismatch
	}
	return tx.Commit()
}
func contentDigest(ctx context.Context, r sqlReader) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, q := range []string{`SELECT id,type,text,ts,cost,attrs FROM nodes ORDER BY id`, `SELECT src,dst,type,weight,ts FROM edges ORDER BY src,dst,type`, `SELECT node_id,model,dim,vec FROM embeddings ORDER BY node_id,model`} {
		if err := enc.Encode(q); err != nil {
			return "", err
		}
		rows, err := r.QueryContext(ctx, q)
		if err != nil {
			return "", err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				return "", err
			}
			if err = enc.Encode(values); err != nil {
				rows.Close()
				return "", err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (e *Engine) publish(ctx context.Context, tx *sql.Tx) (DatasetState, error) {
	s, err := stateFrom(ctx, tx)
	if err != nil {
		return s, err
	}
	if s.ID != e.cfg.DatasetID {
		return s, ErrDatasetMismatch
	}
	digest, err := contentDigest(ctx, tx)
	if err != nil {
		return s, err
	}
	if s.Digest != digest {
		if s.Revision == int64(^uint64(0)>>1) {
			return s, fmt.Errorf("revision exhausted")
		}
		s.Revision++
		s.Digest = digest
		if _, err = tx.ExecContext(ctx, `UPDATE dataset_state SET revision=?,digest=? WHERE singleton=1`, s.Revision, s.Digest); err != nil {
			return s, err
		}
	}
	return s, nil
}
func (e *Engine) checkRevision(ctx context.Context, tx *sql.Tx, id string, revision *int64) error {
	if id != e.cfg.DatasetID {
		return ErrDatasetMismatch
	}
	if revision == nil || *revision < 0 {
		return fmt.Errorf("nonnegative expected_revision is required")
	}
	// Acquire the write lock before checking the revision, including across handles.
	if _, err := tx.ExecContext(ctx, `UPDATE dataset_state SET revision=revision WHERE singleton=1`); err != nil {
		return err
	}
	s, err := stateFrom(ctx, tx)
	if err != nil {
		return err
	}
	if s.Revision != *revision {
		return fmt.Errorf("%w: expected %d, current %d", ErrRevisionConflict, *revision, s.Revision)
	}
	var events int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&events); err != nil {
		return err
	}
	if events > 0 {
		return fmt.Errorf("snapshot updates require a database without an event log; use a new dataset database")
	}
	return nil
}
func completeEdges(ctx context.Context, tx *sql.Tx) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM edges e WHERE NOT EXISTS(SELECT 1 FROM nodes WHERE id=e.src) OR NOT EXISTS(SELECT 1 FROM nodes WHERE id=e.dst)`).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("snapshot contains edges with missing endpoints")
	}
	return nil
}
func (e *Engine) ReplaceSnapshot(ctx context.Context, s Snapshot) (DatasetState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.Batch == nil {
		return DatasetState{}, fmt.Errorf("snapshot batch is required; use an explicit empty batch to clear data")
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return DatasetState{}, err
	}
	defer tx.Rollback()
	if err = e.checkRevision(ctx, tx, s.DatasetID, s.ExpectedRevision); err != nil {
		return DatasetState{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM edges; DELETE FROM nodes`); err != nil {
		return DatasetState{}, err
	}
	if err = putBatch(ctx, tx, *s.Batch); err != nil {
		return DatasetState{}, err
	}
	if err = completeEdges(ctx, tx); err != nil {
		return DatasetState{}, err
	}
	return e.commitSnapshot(ctx, tx)
}
func (e *Engine) Apply(ctx context.Context, u Update) (DatasetState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return DatasetState{}, err
	}
	defer tx.Rollback()
	if err = e.checkRevision(ctx, tx, u.DatasetID, u.ExpectedRevision); err != nil {
		return DatasetState{}, err
	}
	for _, id := range u.DeleteNodes {
		if id == "" {
			return DatasetState{}, fmt.Errorf("delete node id must not be empty")
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM edges WHERE src=? OR dst=?`, id, id); err != nil {
			return DatasetState{}, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id); err != nil {
			return DatasetState{}, err
		}
	}
	for _, ed := range u.DeleteEdges {
		if ed.Src == "" || ed.Dst == "" || ed.Type == "" {
			return DatasetState{}, fmt.Errorf("edge deletion requires src,dst,type")
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM edges WHERE src=? AND dst=? AND type=?`, ed.Src, ed.Dst, ed.Type); err != nil {
			return DatasetState{}, err
		}
	}
	if err = putBatch(ctx, tx, u.Upsert); err != nil {
		return DatasetState{}, err
	}
	if err = completeEdges(ctx, tx); err != nil {
		return DatasetState{}, err
	}
	return e.commitSnapshot(ctx, tx)
}
func (e *Engine) commitSnapshot(ctx context.Context, tx *sql.Tx) (DatasetState, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE dataset_state SET mode='snapshot' WHERE singleton=1`); err != nil {
		return DatasetState{}, err
	}
	s, err := e.publish(ctx, tx)
	if err != nil {
		return s, err
	}
	return s, tx.Commit()
}
func allowEvents(ctx context.Context, tx *sql.Tx) error {
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT mode FROM dataset_state WHERE singleton=1`).Scan(&mode); err != nil {
		return err
	}
	if mode == "snapshot" {
		return fmt.Errorf("ingest/replay is disabled for snapshot-managed datasets")
	}
	return nil
}
