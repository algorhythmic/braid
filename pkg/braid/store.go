package braid

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"time"

	_ "modernc.org/sqlite"
)

// Engine serializes operations to give queries a consistent view across retrievers.
type Engine struct {
	db       *sql.DB
	cfg      Config
	provider Provider
	cost     CostFunc
	mu       contextMutex
	read     *sql.Tx
	stages   map[string]StageStats
}
type Option func(*Engine)

func WithProvider(p Provider) Option { return func(e *Engine) { e.provider = p } }
func WithCost(f CostFunc) Option     { return func(e *Engine) { e.cost = f } }
func Open(ctx context.Context, c Config, opts ...Option) (*Engine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	// Own a deep copy so callers cannot mutate maps during queries.
	b, _ := json.Marshal(c)
	var owned Config
	if err := json.Unmarshal(b, &owned); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", c.Store.Path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	e := &Engine{db: db, cfg: owned}
	if c.Embed.Provider == "ollama" {
		e.provider = &Ollama{URL: c.Embed.URL, ModelName: c.Embed.Model}
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.provider != nil && e.provider.Model() != c.Embed.Model {
		db.Close()
		return nil, fmt.Errorf("provider model must match config embed.model")
	}
	_, err = db.ExecContext(ctx, schema)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = e.initializeDataset(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return e, nil
}
func (e *Engine) Close() error { return e.db.Close() }

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,type TEXT NOT NULL,text TEXT NOT NULL,ts TEXT NOT NULL,cost REAL,attrs TEXT NOT NULL DEFAULT '{}');
CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(text,content='nodes',content_rowid='rowid',tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER IF NOT EXISTS nodes_ai AFTER INSERT ON nodes BEGIN INSERT INTO nodes_fts(rowid,text) VALUES(new.rowid,new.text); END;
CREATE TRIGGER IF NOT EXISTS nodes_ad AFTER DELETE ON nodes BEGIN INSERT INTO nodes_fts(nodes_fts,rowid,text) VALUES('delete',old.rowid,old.text); END;
CREATE TRIGGER IF NOT EXISTS nodes_au AFTER UPDATE ON nodes BEGIN INSERT INTO nodes_fts(nodes_fts,rowid,text) VALUES('delete',old.rowid,old.text); INSERT INTO nodes_fts(rowid,text) VALUES(new.rowid,new.text); END;
CREATE TABLE IF NOT EXISTS edges(src TEXT NOT NULL,dst TEXT NOT NULL,type TEXT NOT NULL,weight REAL NOT NULL,ts TEXT NOT NULL,PRIMARY KEY(src,dst,type));
CREATE TABLE IF NOT EXISTS embeddings(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,model TEXT NOT NULL,dim INTEGER NOT NULL,vec BLOB NOT NULL,PRIMARY KEY(node_id,model));
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY,ts TEXT NOT NULL,name TEXT NOT NULL,payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS labels(query_id TEXT PRIMARY KEY,query TEXT NOT NULL,expected TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS evaluation_labels(query_id TEXT PRIMARY KEY,label TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS eval_runs(id INTEGER PRIMARY KEY,ts TEXT,config_hash TEXT,condition TEXT,metrics TEXT);
CREATE INDEX IF NOT EXISTS nodes_ts ON nodes(ts);
CREATE INDEX IF NOT EXISTS edges_dst ON edges(dst);
`

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func validateNode(n Node) error {
	if n.ID == "" || n.Type == "" || n.TS.IsZero() {
		return fmt.Errorf("node requires id, type, and ts")
	}
	if n.Cost != nil && (!finite(*n.Cost) || *n.Cost < 0) {
		return fmt.Errorf("node %s has invalid cost", n.ID)
	}
	return nil
}
func putBatch(ctx context.Context, tx *sql.Tx, b Batch) error {
	for _, n := range b.Nodes {
		if err := validateNode(n); err != nil {
			return err
		}
		attrs, err := json.Marshal(n.Attrs)
		if err != nil {
			return err
		}
		if n.Attrs == nil {
			attrs = []byte("{}")
		}
		// Text changes invalidate all cached embedding spaces for this node.
		if _, err = tx.ExecContext(ctx, `DELETE FROM embeddings WHERE node_id=? AND EXISTS(SELECT 1 FROM nodes WHERE id=? AND text<>?)`, n.ID, n.ID, n.Text); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO nodes(id,type,text,ts,cost,attrs) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET type=excluded.type,text=excluded.text,ts=excluded.ts,cost=excluded.cost,attrs=excluded.attrs`, n.ID, n.Type, n.Text, timestamp(n.TS), n.Cost, string(attrs))
		if err != nil {
			return err
		}
	}
	for _, ed := range b.Edges {
		if ed.Src == "" || ed.Dst == "" || ed.Type == "" || ed.TS.IsZero() || !finite(ed.Weight) || ed.Weight < 0 {
			return fmt.Errorf("edge requires endpoints, type, ts and nonnegative weight")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO edges VALUES(?,?,?,?,?) ON CONFLICT(src,dst,type) DO UPDATE SET weight=excluded.weight,ts=excluded.ts`, ed.Src, ed.Dst, ed.Type, ed.Weight, timestamp(ed.TS)); err != nil {
			return err
		}
	}
	for _, v := range b.Embeddings {
		if v.NodeID == "" || v.Model == "" || len(v.Vec) == 0 {
			return fmt.Errorf("embedding requires node_id, model and vector")
		}
		var dim int
		err := tx.QueryRowContext(ctx, `SELECT dim FROM embeddings WHERE model=? LIMIT 1`, v.Model).Scan(&dim)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && dim != len(v.Vec) {
			return fmt.Errorf("dimension mismatch for model %s: got %d, want %d", v.Model, len(v.Vec), dim)
		}
		buf := make([]byte, 4*len(v.Vec))
		var norm float64
		for i, x := range v.Vec {
			if !finite(float64(x)) {
				return fmt.Errorf("non-finite embedding")
			}
			norm += float64(x) * float64(x)
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
		}
		if norm == 0 {
			return fmt.Errorf("zero embedding vector")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO embeddings VALUES(?,?,?,?) ON CONFLICT(node_id,model) DO UPDATE SET dim=excluded.dim,vec=excluded.vec`, v.NodeID, v.Model, len(v.Vec), buf); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) Upsert(ctx context.Context, b Batch) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = putBatch(ctx, tx, b); err != nil {
		return err
	}
	_, err = e.publish(ctx, tx)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (e *Engine) nodes(ctx context.Context) ([]Node, error) {
	return e.filteredNodes(ctx, Filters{})
}
func (e *Engine) filteredNodes(ctx context.Context, f Filters) ([]Node, error) {
	start := time.Now()
	where, args, err := filterSQL(f)
	if err != nil {
		return nil, err
	}
	rows, err := e.reader().QueryContext(ctx, `SELECT n.id,n.type,n.text,n.ts,n.cost,n.attrs FROM nodes n WHERE `+where+` ORDER BY n.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		var ts, attrs string
		if err = rows.Scan(&n.ID, &n.Type, &n.Text, &ts, &n.Cost, &attrs); err != nil {
			return nil, err
		}
		n.TS, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(attrs), &n.Attrs); err != nil {
			return nil, err
		}
		e.recordRead("nodes", len(n.ID)+len(n.Type)+len(n.Text)+len(ts)+len(attrs))
		out = append(out, n)
	}
	e.recordTime("nodes", start)
	return out, rows.Err()
}
func (e *Engine) vectors(ctx context.Context) (map[string][]float32, error) {
	return e.vectorIDs(ctx, nil)
}
func (e *Engine) vectorIDs(ctx context.Context, ids []string) (map[string][]float32, error) {
	start := time.Now()
	if e.cfg.Embed.Model == "" || (ids != nil && len(ids) == 0) {
		return map[string][]float32{}, nil
	}
	var foreign int
	if err := e.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings WHERE model<>?`, e.cfg.Embed.Model).Scan(&foreign); err != nil {
		return nil, err
	}
	if foreign > 0 {
		return nil, fmt.Errorf("stored embeddings use another model; reindex required before querying model %s", e.cfg.Embed.Model)
	}
	query := `SELECT node_id,dim,vec FROM embeddings WHERE model=?`
	args := []any{e.cfg.Embed.Model}
	if ids != nil {
		b, _ := json.Marshal(ids)
		query += ` AND node_id IN (SELECT value FROM json_each(?))`
		args = append(args, string(b))
	}
	rows, err := e.reader().QueryContext(ctx, query+` ORDER BY node_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float32{}
	dim := 0
	for rows.Next() {
		var id string
		var d int
		var b []byte
		if err = rows.Scan(&id, &d, &b); err != nil {
			return nil, err
		}
		if d <= 0 || len(b) != d*4 || (dim != 0 && d != dim) {
			return nil, fmt.Errorf("corrupt or mixed embedding dimensions")
		}
		dim = d
		v := make([]float32, d)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		}
		e.recordRead("vectors", len(id)+len(b))
		out[id] = v
	}
	e.recordTime("vectors", start)
	return out, rows.Err()
}
