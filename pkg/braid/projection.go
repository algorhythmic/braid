package braid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"
)

type Projection struct {
	On    string     `yaml:"on"`
	Nodes []NodeRule `yaml:"nodes"`
	Edges []EdgeRule `yaml:"edges"`
}
type NodeRule struct {
	Type string   `yaml:"type"`
	ID   string   `yaml:"id"`
	Text []string `yaml:"text"`
	TS   string   `yaml:"ts"`
	Each string   `yaml:"each"`
}
type EdgeRule struct {
	From   string   `yaml:"from"`
	To     string   `yaml:"to"`
	Type   string   `yaml:"type"`
	Weight *float64 `yaml:"weight"`
	Each   string   `yaml:"each"`
}

func render(s string, root map[string]any, item any) (string, error) {
	// Bind $event to the payload while dot is the current each item.
	t, err := template.New("projection").Option("missingkey=error").Parse(`{{$event := .Root}}{{range .Items}}` + s + `{{end}}`)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, struct {
		Root  map[string]any
		Items []any
	}{root, []any{item}})
	return b.String(), err
}
func field(m map[string]any, path string) (any, bool) {
	var v any = m
	for _, p := range strings.Split(path, ".") {
		a, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = a[p]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func eachItems(root map[string]any, path string) ([]any, error) {
	if path == "" {
		return []any{root}, nil
	}
	v, ok := field(root, path)
	if !ok {
		return nil, fmt.Errorf("missing each field %s", path)
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("each field %s must be a list", path)
	}
	return a, nil
}
func (e *Engine) project(ev Event) (Batch, error) {
	b := Batch{}
	root := map[string]any{}
	for k, v := range ev.Payload {
		root[k] = v
	}
	if _, ok := root["ts"]; !ok {
		root["ts"] = ev.TS.Format(time.RFC3339Nano)
	}
	for _, p := range e.cfg.Projection {
		if p.On != ev.Name {
			continue
		}
		for _, r := range p.Nodes {
			items, err := eachItems(root, r.Each)
			if err != nil {
				return b, err
			}
			for _, it := range items {
				id, err := render(r.ID, root, it)
				if err != nil {
					return b, err
				}
				ts := ev.TS
				if r.TS != "" {
					s, er := render(r.TS, root, it)
					if er != nil {
						return b, er
					}
					ts, er = time.Parse(time.RFC3339Nano, s)
					if er != nil {
						return b, er
					}
				}
				attrs := root
				if m, ok := it.(map[string]any); ok {
					attrs = m
				}
				fields := r.Text
				if len(fields) == 0 {
					fields = e.cfg.Index.Nodes[r.Type]
				}
				parts := []string{}
				for _, f := range fields {
					if v, ok := field(attrs, f); ok {
						parts = append(parts, fmt.Sprint(v))
					}
				}
				b.Nodes = append(b.Nodes, Node{ID: id, Type: r.Type, Text: strings.Join(parts, "\n"), TS: ts, Attrs: attrs})
			}
		}
		for _, r := range p.Edges {
			items, err := eachItems(root, r.Each)
			if err != nil {
				return b, err
			}
			for _, it := range items {
				// From uses the root so report:{{.id}} works with scalar each entries.
				from, err := render(r.From, root, root)
				if err != nil {
					return b, err
				}
				to, err := render(r.To, root, it)
				if err != nil {
					return b, err
				}
				w := 1.
				if r.Weight != nil {
					w = *r.Weight
				}
				b.Edges = append(b.Edges, Edge{Src: from, Dst: to, Type: r.Type, Weight: w, TS: ev.TS})
			}
		}
	}
	return b, nil
}

// Ingest atomically appends and projects events. Reusing an ID with identical
// content is idempotent; conflicting content is rejected.
func (e *Engine) Ingest(ctx context.Context, events []Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = allowEvents(ctx, tx); err != nil {
		return err
	}
	for _, ev := range events {
		if ev.ID <= 0 || ev.Name == "" || ev.TS.IsZero() {
			return fmt.Errorf("event requires positive id, name and ts")
		}
		payload, err := json.Marshal(ev.Payload)
		if err != nil {
			return err
		}
		// Project the same JSON representation that replay will read. This also
		// accepts ordinary Go slices such as []string in library event payloads.
		if err = json.Unmarshal(payload, &ev.Payload); err != nil {
			return err
		}
		var maxID int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM events`).Scan(&maxID); err != nil {
			return err
		}
		if ev.ID <= maxID {
			var ts, name, old string
			err = tx.QueryRowContext(ctx, `SELECT ts,name,payload FROM events WHERE id=?`, ev.ID).Scan(&ts, &name, &old)
			if err != nil || ts != timestamp(ev.TS) || name != ev.Name || old != string(payload) {
				return fmt.Errorf("event %d conflicts or is out of order", ev.ID)
			}
			continue
		}
		b, err := e.project(ev)
		if err != nil {
			return fmt.Errorf("event %d: %w", ev.ID, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events VALUES(?,?,?,?)`, ev.ID, timestamp(ev.TS), ev.Name, string(payload)); err != nil {
			return err
		}
		if err = putBatch(ctx, tx, b); err != nil {
			return err
		}
	}
	if _, err = e.publish(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Replay replaces the entire node/edge projection. Direct upserts are not logged
// and are therefore removed. Cached vectors survive only for identical text.
func (e *Engine) Replay(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = allowEvents(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,ts,name,payload FROM events ORDER BY id`)
	if err != nil {
		return err
	}
	events := []Event{}
	for rows.Next() {
		var ev Event
		var ts, p string
		if err = rows.Scan(&ev.ID, &ts, &ev.Name, &p); err != nil {
			rows.Close()
			return err
		}
		ev.TS, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(p), &ev.Payload); err != nil {
			rows.Close()
			return err
		}
		events = append(events, ev)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE replay_vectors AS SELECT e.*,n.text FROM embeddings e JOIN nodes n ON n.id=e.node_id; DELETE FROM embeddings; DELETE FROM edges; DELETE FROM nodes;`)
	if err != nil {
		return err
	}
	for _, ev := range events {
		b, er := e.project(ev)
		if er != nil {
			return er
		}
		if er = putBatch(ctx, tx, b); er != nil {
			return er
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO embeddings SELECT v.node_id,v.model,v.dim,v.vec FROM replay_vectors v JOIN nodes n ON n.id=v.node_id AND n.text=v.text; DROP TABLE replay_vectors;`)
	if err != nil {
		return err
	}
	if _, err = e.publish(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
