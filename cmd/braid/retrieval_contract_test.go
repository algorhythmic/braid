package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/algorhythmic/braid/pkg/braid"
)

type timeoutProvider struct{}

func (timeoutProvider) Model() string { return "timeout-fixture" }
func (timeoutProvider) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRetrievalProtocolContractsAndTimeoutRecovery(t *testing.T) {
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	c.Embed.Model = "timeout-fixture"
	e, err := braid.Open(context.Background(), c, braid.WithProvider(timeoutProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ready := false
	for _, tc := range []struct{ input, code string }{
		{`{"id":1,"method":"hello","versions":[1],"required_capabilities":["hard_filters","allowed_ids","get_many","read_revision","required_context","context_export","query_timeout","stage_stats","constrained_eval"]}`, ""},
		{`{"id":2,"method":"upsert","batch":{"nodes":[{"id":"N","type":"binding","text":"find","ts":"2026-09-04T12:00:00Z","attrs":{"key":"N","modes":["n"]}}]}}`, ""},
		{`{"id":3,"method":"get_many","get":{"dataset_id":"default","ids":["N","n","?"],"expected_revision":1}}`, ""},
		{`{"id":4,"method":"get_many","get":{"dataset_id":"default","ids":["N"],"expected_revision":0}}`, "revision_conflict"},
		{`{"id":5,"method":"get_many","get":{"ids":["N"]}}`, "dataset_mismatch"},
		{`{"id":6,"method":"query","query":{"expected_revision":0,"text":"find"}}`, "revision_conflict"},
		{`{"id":7,"method":"query","timeout_ms":10,"query":{"text":"find"}}`, "cancelled"},
		{`{"id":8,"method":"query","timeout_ms":0,"query":{}}`, "invalid_request"},
		{`{"id":9,"method":"query","timeout_ms":86400001,"query":{}}`, "invalid_request"},
		{`{"id":10,"method":"dataset","timeout_ms":1}`, "invalid_request"},
		{`{"id":11,"method":"query","query":{"weights":{"dense":0,"graph":0,"temporal":0},"text":"find","filters":{"allowed_ids":[]}}}`, ""},
		{`{"id":12,"method":"export_context","export":{"query":{"text":"find","weights":{"dense":0,"graph":0,"temporal":0},"filters":{"predicate":{"op":"contains","path":"modes","value":"n"}}},"output":{"fields":["id","attrs"],"attrs":["key"],"max_bytes":100}}}`, ""},
	} {
		r := handleRequest(context.Background(), e, []byte(tc.input), &ready)
		code := ""
		if r.Error != nil {
			code = r.Error.Code
		}
		if code != tc.code {
			t.Fatalf("%s: %+v", tc.input, r)
		}
		if string(r.ID) == "11" && len(r.Result.(braid.Result).Items) != 0 {
			t.Fatal(r)
		}
		if string(r.ID) == "12" {
			export := r.Result.(braid.ExportResult)
			if export.Abstained || export.ContextBytes > 100 {
				t.Fatal(export)
			}
		}
	}
	// A real sequential session emits exactly one full response per request and
	// continues after a request-scoped deadline expires.
	input := strings.NewReader(`{"id":1,"method":"hello","versions":[1]}` + "\n" + `{"id":2,"method":"query","timeout_ms":10,"query":{"text":"find"}}` + "\n" + `{"id":3,"method":"dataset"}` + "\n")
	var out bytes.Buffer
	start := time.Now()
	if err = serve(context.Background(), e, input, &out); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline did not bound request")
	}
	dec := json.NewDecoder(&out)
	for i := 1; i <= 3; i++ {
		var r response
		if err = dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if r.Error == nil || r.Error.Code != "cancelled" {
				t.Fatal(r)
			}
		} else if r.Error != nil {
			t.Fatal(r)
		}
	}
	var extra response
	if err = dec.Decode(&extra); err != io.EOF {
		t.Fatal("extra or incomplete response", err)
	}
}
