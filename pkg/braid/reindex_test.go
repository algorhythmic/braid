package braid

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type gatedProvider struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *gatedProvider) Model() string { return "fixture-v1" }
func (p *gatedProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	p.calls.Add(1)
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := [][]float32{}
	for range texts {
		out = append(out, []float32{1, 0})
	}
	return out, nil
}
func TestReindexReuseAndForce(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	p := &gatedProvider{}
	e := openTest(t, c, WithProvider(p))
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "task", "one", 1), node("b", "task", "two", 1)}, Embeddings: []Embedding{{NodeID: "a", Model: p.Model(), Vec: []float32{1, 0}}}})
	r, err := e.ReindexWithOptions(testCtx, ReindexOptions{})
	if err != nil || r.Reused != 1 || r.Generated != 1 || p.calls.Load() != 1 {
		t.Fatal(r, err)
	}
	again, err := e.ReindexWithOptions(testCtx, ReindexOptions{})
	if err != nil || again.Reused != 2 || again.Generated != 0 || p.calls.Load() != 1 || again.Dataset != r.Dataset {
		t.Fatal(again, err)
	}
	forced, err := e.ReindexWithOptions(testCtx, ReindexOptions{Force: true})
	if err != nil || forced.Generated != 2 || forced.Reused != 0 || p.calls.Load() != 2 {
		t.Fatal(forced, err)
	}
	mustUpsert(t, e, Batch{Nodes: []Node{node("b", "task", "changed", 1)}})
	changed, err := e.ReindexWithOptions(testCtx, ReindexOptions{})
	if err != nil || changed.Generated != 1 || changed.Reused != 1 {
		t.Fatal(changed, err)
	}
}
func TestReindexAllowsReadersAndRejectsStaleBuild(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	p := &gatedProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	e := openTest(t, c, WithProvider(p))
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "task", "one", 1), node("b", "task", "two", 1)}, Embeddings: []Embedding{{NodeID: "a", Model: p.Model(), Vec: []float32{1, 0}}}})
	done := make(chan error, 1)
	go func() { done <- e.Reindex(testCtx) }()
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("build did not start")
	}
	ctx, cancel := context.WithTimeout(testCtx, time.Second)
	defer cancel()
	r, err := e.Query(ctx, Query{Anchors: []string{"a"}, At: testTime})
	if err != nil || len(r.Items) == 0 {
		close(p.release)
		t.Fatal("reader blocked", err)
	}
	if err = e.Upsert(ctx, Batch{Nodes: []Node{node("b", "task", "newer", 1)}}); err != nil {
		close(p.release)
		t.Fatal("writer blocked", err)
	}
	close(p.release)
	select {
	case err = <-done:
		if !errors.Is(err, ErrRevisionConflict) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("build did not finish")
	}
	vs, err := e.vectors(testCtx)
	if err != nil || len(vs) != 1 {
		t.Fatal("stale build published", vs, err)
	}
}
func TestCancelledReindexKeepsPublishedVectors(t *testing.T) {
	c := DefaultConfig()
	c.Embed.Model = "fixture-v1"
	p := &gatedProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	e := openTest(t, c, WithProvider(p))
	mustUpsert(t, e, Batch{Nodes: []Node{node("a", "task", "one", 1)}, Embeddings: []Embedding{{NodeID: "a", Model: p.Model(), Vec: []float32{0, 1}}}})
	before, _ := e.Dataset(testCtx)
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.ReindexWithOptions(ctx, ReindexOptions{Force: true}); done <- err }()
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("build did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation ignored")
	}
	after, _ := e.Dataset(testCtx)
	if before != after {
		t.Fatal("cancelled build changed publication")
	}
}
