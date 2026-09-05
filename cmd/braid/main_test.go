package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/algorhythmic/braid/pkg/braid"
)

func TestStdioRecoversAfterMalformedRequest(t *testing.T) {
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(t.TempDir(), "stdio.db")
	e, err := braid.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	in := strings.NewReader(`{"id":"hello","method":"hello","versions":[1]}` + "\n{bad json}\n" + `{"id":2,"method":"upsert","batch":{"nodes":[{"id":"a","type":"report","text":"queue failure","ts":"2026-09-04T12:00:00Z"}]}}` + "\n" + `{"id":3,"method":"query","query":{"text":"queue","at":"2026-09-04T12:00:00Z"}}` + "\n")
	var out bytes.Buffer
	if err = serve(context.Background(), e, in, &out); err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(&out)
	var res map[string]any
	if err = d.Decode(&res); err != nil || res["error"] != nil {
		t.Fatal("hello failed", err, res)
	}
	res = nil
	if err = d.Decode(&res); err != nil || res["error"] == nil {
		t.Fatal("missing parse error", err, res)
	}
	res = nil
	if err = d.Decode(&res); err != nil || res["id"] != float64(2) || res["error"] != nil {
		t.Fatal("upsert", err, res)
	}
	res = nil
	if err = d.Decode(&res); err != nil || res["id"] != float64(3) || res["error"] != nil {
		t.Fatal("query", err, res)
	}
	result := res["result"].(map[string]any)
	if len(result["items"].([]any)) != 1 {
		t.Fatal("missing result")
	}
}
