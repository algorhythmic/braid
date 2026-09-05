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

func TestProtocolNegotiationAndStrictRequests(t *testing.T) {
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	e, err := braid.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ready := false
	cases := []struct{ in, code string }{
		{`{"id":1,"method":"upsert","batch":{}}`, "negotiation_required"},
		{`{"id":1,"method":"hello","versions":[99]}`, "version_not_supported"},
		{`{"id":1,"method":"hello","versions":[1],"required_capabilities":["datasets"]}`, "capability_not_supported"},
		{`{"id":1,"method":"hello","versions":[1],"required_capabilities":["strict_requests"]}`, ""},
		{`{"id":1,"method":"hello","versions":[1]}`, "already_negotiated"},
		{`{"id":2,"method":"query","query":{"filters":{"account":"other"}}}`, "invalid_request"},
		{`{"id":2,"method":"query","query":{"Text":"case alias"}}`, "invalid_request"},
		{`{"id":2,"method":"query","query":{"text":"a","text":"b"}}`, "invalid_request"},
		{`{"id":2,"id":3,"method":"query","query":{}}`, "invalid_request"},
		{`{"id":2,"method":"query","query":null}`, "invalid_request"},
		{`{"id":2,"method":"query"}`, "invalid_request"},
		{`{"id":2,"method":"replay","batch":{}}`, "invalid_request"},
		{`{"id":null,"method":"query","query":{}}`, "invalid_request"},
		{`{"id":{},"method":"query","query":{}}`, "invalid_request"},
		{`{"id":2,"method":"unknown"}`, "unknown_method"},
		{`{"id":2,"method":"query","query":{"budget":{"max":-1}}}`, "operation_failed"},
		{`{"id":2,"method":"upsert","batch":{"nodes":[{"id":"a","type":"task","text":"needle","ts":"2026-09-04T12:00:00Z","cost":null,"attrs":{"arbitrary":{"nested":true}}}]}}`, ""},
		{`{"id":9007199254740993,"method":"query","query":{"text":"needle"}}`, ""},
	}
	for _, tc := range cases {
		r := handleRequest(context.Background(), e, []byte(tc.in), &ready)
		code := ""
		if r.Error != nil {
			code = r.Error.Code
			if r.Result != nil {
				t.Fatal("both result and error")
			}
		}
		if code != tc.code {
			t.Fatalf("%s: got %+v want %s", tc.in, r, tc.code)
		}
		if strings.Contains(tc.in, "9007199254740993") && string(r.ID) != "9007199254740993" {
			t.Fatal("lost ID precision")
		}
	}
}
func TestProtocolOversizeRecovery(t *testing.T) {
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	e, err := braid.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	in := strings.NewReader(strings.Repeat("x", maxRequestBytes+1) + "\n" + `{"id":1,"method":"hello","versions":[1]}` + "\n")
	var out bytes.Buffer
	if err = serve(context.Background(), e, in, &out); err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(&out)
	var a, b response
	if err = d.Decode(&a); err != nil {
		t.Fatal(err)
	}
	if err = d.Decode(&b); err != nil {
		t.Fatal(err)
	}
	if a.Error == nil || a.Error.Code != "request_too_large" || b.Error != nil {
		t.Fatal(a, b)
	}
}

func TestSnapshotProtocolRevisionAndScope(t *testing.T) {
	c := braid.DefaultConfig()
	c.DatasetID = "heimdall:test"
	c.Store.Path = filepath.Join(t.TempDir(), "db")
	e, err := braid.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ready := false
	for _, tc := range []struct{ input, code string }{
		{`{"id":1,"method":"hello","versions":[1],"required_capabilities":["dataset_binding","replace_snapshot","apply"]}`, ""},
		{`{"id":2,"method":"replace_snapshot","snapshot":{"dataset_id":"heimdall:test","expected_revision":0,"batch":{"nodes":[{"id":"a","type":"task","text":"work","ts":"2026-09-04T12:00:00Z"}]}}}`, ""},
		{`{"id":3,"method":"apply","update":{"dataset_id":"other","expected_revision":1,"delete_nodes":["a"]}}`, "dataset_mismatch"},
		{`{"id":4,"method":"apply","update":{"dataset_id":"heimdall:test","expected_revision":0,"delete_nodes":["a"]}}`, "revision_conflict"},
		{`{"id":5,"method":"query","query":{"dataset_id":"other"}}`, "dataset_mismatch"},
		{`{"id":6,"method":"apply","update":{"dataset_id":"heimdall:test","expected_revision":1,"delete_nodes":["a"]}}`, ""},
	} {
		res := handleRequest(context.Background(), e, []byte(tc.input), &ready)
		got := ""
		if res.Error != nil {
			got = res.Error.Code
		}
		if got != tc.code {
			t.Fatal(tc, res)
		}
	}
	state, err := e.Dataset(context.Background())
	if err != nil || state.Revision != 2 {
		t.Fatal(state, err)
	}
}
