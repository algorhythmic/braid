package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/algorhythmic/braid/pkg/braid"
)

type jobProvider struct{ entered chan struct{} }

func (p *jobProvider) Model() string { return "test" }
func (p *jobProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestBackgroundJobDoesNotBlockRequests(t *testing.T) {
	c := braid.DefaultConfig()
	c.Embed.Model = "test"
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	p := &jobProvider{entered: make(chan struct{}, 1)}
	e, err := braid.Open(context.Background(), c, braid.WithProvider(p))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Upsert(context.Background(), braid.Batch{Nodes: []braid.Node{{ID: "a", Type: "task", Text: "text", TS: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	ready := false
	jobs := &reindexJobs{}
	defer jobs.close()
	call := func(input string) response {
		return handleRequest(context.Background(), e, []byte(input), &ready, jobs)
	}
	if r := call(`{"id":1,"method":"hello","versions":[1],"required_capabilities":["reindex_start"]}`); r.Error != nil {
		t.Fatal(r)
	}
	if r := call(`{"id":2,"method":"reindex_start"}`); r.Error != nil {
		t.Fatal(r)
	}
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := handleRequest(ctx, e, []byte(`{"id":3,"method":"query","query":{}}`), &ready, jobs)
	if r.Error != nil {
		t.Fatal("query blocked by job", r)
	}
	if r = call(`{"id":4,"method":"reindex_start"}`); r.Error == nil || r.Error.Code != "job_running" {
		t.Fatal(r)
	}
	if r = call(`{"id":5,"method":"reindex_status","job_id":"missing"}`); r.Error == nil || r.Error.Code != "unknown_job" {
		t.Fatal(r)
	}
	if r = call(`{"id":6,"method":"reindex_cancel","job_id":"reindex:1"}`); r.Error != nil {
		t.Fatal(r)
	}
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status := jobs.current.snapshot()
		if status.State == "cancelled" {
			if status.Error.Code != "cancelled" {
				t.Fatal(status)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("job did not cancel")
		case <-ticker.C:
		}
	}
}
