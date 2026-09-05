package braid

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func revision(n int64) *int64 { return &n }
func TestDatasetBindingAndRetrieverIsolation(t *testing.T) {
	c := DefaultConfig()
	c.DatasetID = "account-a"
	c.Embed.Model = "fixture-v1"
	a := openTest(t, c, WithProvider(fixtureProvider{}))
	c.DatasetID = "account-b"
	b := openTest(t, c, WithProvider(fixtureProvider{}))
	mustUpsert(t, a, Batch{Nodes: []Node{node("local", "task", "public repair", 1)}, Edges: []Edge{{Src: "local", Dst: "foreign", Type: "link", Weight: 1, TS: testTime}}, Embeddings: []Embedding{{NodeID: "local", Model: "fixture-v1", Vec: []float32{1, 0}}}})
	mustUpsert(t, b, Batch{Nodes: []Node{node("foreign", "task", "secret invoice", 1)}, Embeddings: []Embedding{{NodeID: "foreign", Model: "fixture-v1", Vec: []float32{0, 1}}}})
	wrong := a.cfg
	wrong.DatasetID = "account-b"
	if other, err := Open(testCtx, wrong, WithProvider(fixtureProvider{})); err == nil {
		other.Close()
		t.Fatal("rebound another dataset")
	}
	for _, name := range retrieverNames {
		r, _ := a.Retriever(name)
		if _, err := r.Retrieve(testCtx, Query{DatasetID: "account-b"}, 10); !errors.Is(err, ErrDatasetMismatch) {
			t.Fatal(name, err)
		}
		hits, err := r.Retrieve(testCtx, Query{Anchors: []string{"local", "foreign"}, At: testTime}, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			if h.ID == "foreign" {
				t.Fatal("cross-dataset hit", name)
			}
		}
	}
	r, _ := a.Retriever("lexical")
	hits, err := r.Retrieve(testCtx, Query{Text: "secret"}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("foreign FTS content", hits, err)
	}
	if _, err = a.Query(testCtx, Query{DatasetID: "account-b"}); !errors.Is(err, ErrDatasetMismatch) {
		t.Fatal(err)
	}
	state := mustQuery(t, a, Query{}).Dataset
	if state.ID != "account-a" || state.Revision != 1 || state.Digest == "" {
		t.Fatal(state)
	}
}
func TestSnapshotAndDeletionAtomicity(t *testing.T) {
	c := DefaultConfig()
	c.DatasetID = "scope"
	c.Embed.Model = "fixture-v1"
	e := openTest(t, c, WithProvider(fixtureProvider{}))
	batch := Batch{Nodes: []Node{node("a", "task", "keep", 1), node("b", "task", "obsolete", 1)}, Edges: []Edge{{Src: "a", Dst: "b", Type: "link", Weight: 1, TS: testTime}}, Embeddings: []Embedding{{NodeID: "a", Model: "fixture-v1", Vec: []float32{1, 0}}, {NodeID: "b", Model: "fixture-v1", Vec: []float32{0, 1}}}}
	s, err := e.ReplaceSnapshot(testCtx, Snapshot{DatasetID: "scope", ExpectedRevision: revision(0), Batch: &batch})
	if err != nil || s.Revision != 1 {
		t.Fatal(s, err)
	}
	if _, err = e.Apply(testCtx, Update{DatasetID: "scope", ExpectedRevision: revision(0), DeleteNodes: []string{"a"}}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale mutation accepted", err)
	}
	bad := Snapshot{DatasetID: "scope", ExpectedRevision: revision(1), Batch: &Batch{Nodes: []Node{node("c", "task", "replacement", 1)}, Edges: []Edge{{Src: "c", Dst: "foreign", Type: "link", Weight: 1, TS: testTime}}}}
	if _, err = e.ReplaceSnapshot(testCtx, bad); err == nil {
		t.Fatal("dangling snapshot accepted")
	}
	state, _ := e.Dataset(testCtx)
	if state != s {
		t.Fatal("failed snapshot changed revision", state)
	}
	if _, err = e.Apply(testCtx, Update{DatasetID: "scope", ExpectedRevision: revision(1), DeleteNodes: []string{"a"}, Upsert: Batch{Nodes: []Node{{ID: "invalid"}}}}); err == nil {
		t.Fatal("invalid update accepted")
	}
	r := mustQuery(t, e, Query{})
	if len(r.Items) != 2 || r.Dataset != s {
		t.Fatal("failed mutation damaged data")
	}
	s, err = e.Apply(testCtx, Update{DatasetID: "scope", ExpectedRevision: revision(1), DeleteNodes: []string{"b"}})
	if err != nil || s.Revision != 2 {
		t.Fatal(s, err)
	}
	for table, want := range map[string]int{"nodes": 1, "edges": 0, "embeddings": 1} {
		var count int
		if err = e.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, err)
		}
	}
	lex, _ := e.Retriever("lexical")
	hits, err := lex.Retrieve(testCtx, Query{Text: "obsolete"}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("stale deleted FTS", hits, err)
	}
	if err = e.Replay(testCtx); err == nil {
		t.Fatal("replay erased snapshot")
	}
	if err = e.Ingest(testCtx, []Event{{ID: 1, TS: testTime, Name: "anything"}}); err == nil {
		t.Fatal("events mixed with snapshot")
	}
	same, err := e.Apply(testCtx, Update{DatasetID: "scope", ExpectedRevision: revision(2), DeleteNodes: []string{"missing"}})
	if err != nil || same != s {
		t.Fatal("no-op changed revision", same, err)
	}
}
func TestEdgeRemovalAndConcurrentReaderSnapshot(t *testing.T) {
	c := DefaultConfig()
	c.DatasetID = "shared-scope"
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	e, err := Open(testCtx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	other, err := Open(testCtx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = e.ReplaceSnapshot(testCtx, Snapshot{DatasetID: c.DatasetID, ExpectedRevision: revision(0), Batch: &Batch{Nodes: []Node{node("a", "task", "old", 1), node("b", "task", "old", 1)}, Edges: []Edge{{Src: "a", Dst: "b", Type: "link", Weight: 1, TS: testTime}}}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.db.BeginTx(testCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	view := &Engine{db: e.db, cfg: c, read: tx}
	before, err := view.query(testCtx, Query{At: testTime})
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Apply(testCtx, Update{DatasetID: c.DatasetID, ExpectedRevision: revision(1), DeleteEdges: []EdgeKey{{Src: "a", Dst: "b", Type: "link"}}, Upsert: Batch{Nodes: []Node{node("a", "task", "new", 1)}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := view.query(testCtx, Query{At: testTime})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reader mixed revisions", err)
	}
	tx.Rollback()
	current := mustQuery(t, e, Query{})
	if current.Dataset.Revision != 2 {
		t.Fatal(current.Dataset)
	}
	graph, _ := e.Retriever("graph")
	hits, err := graph.Retrieve(testCtx, Query{Anchors: []string{"a"}}, 10)
	if err != nil || len(hits) != 1 {
		t.Fatal("removed edge traversed", hits, err)
	}
	if _, err = e.Apply(testCtx, Update{DatasetID: c.DatasetID, ExpectedRevision: revision(1)}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("cross-handle stale write", err)
	}
}
func TestLegacyDatasetCannotBeRelabeled(t *testing.T) {
	c := DefaultConfig()
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "task", "legacy", 1)}})
	if _, err := e.db.Exec(`DROP TABLE dataset_state`); err != nil {
		t.Fatal(err)
	}
	c = e.cfg
	c.DatasetID = "new-owner"
	if other, err := Open(testCtx, c); err == nil {
		other.Close()
		t.Fatal("legacy content silently relabeled")
	}
	c.DatasetID = "default"
	other, err := Open(testCtx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if state, err := other.Dataset(testCtx); err != nil || state.ID != "default" {
		t.Fatal(state, err)
	}
}
