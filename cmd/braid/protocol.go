package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/algorhythmic/braid/pkg/braid"
)

const protocolVersion = 1
const maxRequestBytes = 16 << 20

var capabilities = []string{"query", "explain", "upsert", "ingest", "replay", "reindex", "eval", "strict_requests", "structured_errors", "dataset_binding", "replace_snapshot", "apply", "dataset", "reindex_start", "reindex_status", "reindex_cancel", "embedding_reuse", "negative_eval", "abstention_policy"}

type request struct {
	Force                bool            `json:"force"`
	JobID                string          `json:"job_id"`
	Snapshot             braid.Snapshot  `json:"snapshot"`
	Update               braid.Update    `json:"update"`
	ID                   json.RawMessage `json:"id"`
	Method               string          `json:"method"`
	Versions             []int           `json:"versions"`
	RequiredCapabilities []string        `json:"required_capabilities"`
	Query                braid.Query     `json:"query"`
	Events               []braid.Event   `json:"events"`
	Batch                braid.Batch     `json:"batch"`
	Labels               []braid.Label   `json:"labels"`
}
type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type response struct {
	Version int             `json:"protocol_version"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *protocolError  `json:"error,omitempty"`
}

func fail(id json.RawMessage, code, message string) response {
	return response{Version: protocolVersion, ID: id, Error: &protocolError{Code: code, Message: message}}
}

// Strict object parsing preserves raw IDs, rejects duplicate keys (even in attrs),
// and checks exact field spelling rather than encoding/json's case folding.
func strictDecode(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := uniqueValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	if err := exactFields(b, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func uniqueValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		seen := map[string]bool{}
		for d.More() {
			key, er := d.Token()
			if er != nil {
				return er
			}
			s, ok := key.(string)
			if !ok {
				return fmt.Errorf("object key must be a string")
			}
			if seen[s] {
				return fmt.Errorf("duplicate field %q", s)
			}
			seen[s] = true
			if er = uniqueValue(d); er != nil {
				return er
			}
		}
	} else if delim == '[' {
		for d.More() {
			if err = uniqueValue(d); err != nil {
				return err
			}
		}
	} else {
		return fmt.Errorf("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}
func exactFields(b []byte, t reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		if t.Kind() == reflect.Pointer || t.Kind() == reflect.Map || t.Kind() == reflect.Slice || t.Kind() == reflect.Interface {
			return nil
		}
		return fmt.Errorf("null is not valid for %s", t)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) || reflect.PointerTo(t).Implements(reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			return err
		}
		known := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			known[name] = f.Type
		}
		for name, raw := range fields {
			typ, ok := known[name]
			if !ok {
				return fmt.Errorf("unknown field %q", name)
			}
			if err := exactFields(raw, typ); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(b, &values); err != nil {
			return err
		}
		for i, v := range values {
			if err := exactFields(v, t.Elem()); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(b, &values); err != nil {
			return err
		}
		for name, v := range values {
			if err := exactFields(v, t.Elem()); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}
func validateRequest(b []byte) (request, *protocolError) {
	var r request
	if !json.Valid(b) {
		return r, &protocolError{Code: "parse_error", Message: "request is not valid JSON"}
	}
	if len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return r, &protocolError{Code: "invalid_request", Message: "request must be an object"}
	}
	if err := strictDecode(b, &r); err != nil {
		return request{}, &protocolError{Code: "invalid_request", Message: err.Error()}
	}
	var id any
	d := json.NewDecoder(bytes.NewReader(r.ID))
	d.UseNumber()
	if err := d.Decode(&id); err != nil {
		return request{}, &protocolError{Code: "invalid_request", Message: "id must be a nonempty string or number"}
	}
	switch v := id.(type) {
	case string:
		if v == "" {
			return request{}, &protocolError{Code: "invalid_request", Message: "id must not be empty"}
		}
	case json.Number:
	default:
		return request{}, &protocolError{Code: "invalid_request", Message: "id must be a string or number"}
	}
	allowed := map[string]bool{"id": true, "method": true}
	required := ""
	switch r.Method {
	case "hello":
		allowed["versions"] = true
		allowed["required_capabilities"] = true
		required = "versions"
	case "query", "explain":
		required = "query"
	case "ingest":
		required = "events"
	case "upsert":
		required = "batch"
	case "eval":
		required = "labels"
	case "replace_snapshot":
		required = "snapshot"
	case "apply":
		required = "update"
	case "reindex", "reindex_start":
		allowed["force"] = true
	case "reindex_status", "reindex_cancel":
		required = "job_id"
	case "replay", "dataset":
	default:
		return r, &protocolError{Code: "unknown_method", Message: fmt.Sprintf("unknown method %q", r.Method)}
	}
	if required != "" {
		allowed[required] = true
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	for key := range fields {
		if !allowed[key] {
			return r, &protocolError{Code: "invalid_request", Message: fmt.Sprintf("field %q is not allowed for %s", key, r.Method)}
		}
	}
	if required != "" {
		raw, ok := fields[required]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return r, &protocolError{Code: "invalid_request", Message: required + " is required and must not be null"}
		}
	}
	if r.Method == "hello" && len(r.Versions) == 0 {
		return r, &protocolError{Code: "invalid_request", Message: "versions must not be empty"}
	}
	return r, nil
}
func handleRequest(ctx context.Context, e *braid.Engine, b []byte, ready *bool, managers ...*reindexJobs) response {
	r, problem := validateRequest(b)
	if problem != nil {
		return fail(r.ID, problem.Code, problem.Message)
	}
	if r.Method == "hello" {
		if *ready {
			return fail(r.ID, "already_negotiated", "protocol has already been negotiated")
		}
		supported := false
		for _, v := range r.Versions {
			if v == protocolVersion {
				supported = true
			}
		}
		if !supported {
			return fail(r.ID, "version_not_supported", "supported protocol versions: [1]")
		}
		for _, want := range r.RequiredCapabilities {
			found := false
			for _, have := range capabilities {
				if want == have {
					found = true
				}
			}
			if !found {
				return fail(r.ID, "capability_not_supported", "unsupported required capability: "+want)
			}
		}
		state, err := e.Dataset(ctx)
		if err != nil {
			return fail(r.ID, "operation_failed", err.Error())
		}
		*ready = true
		return response{Version: protocolVersion, ID: r.ID, Result: map[string]any{"protocol_version": protocolVersion, "capabilities": capabilities, "max_request_bytes": maxRequestBytes, "ranking_version": "mmr-normalized-jaccard-v1", "dataset": state}}
	}
	if !*ready {
		return fail(r.ID, "negotiation_required", "send hello with supported versions before other methods")
	}
	resp := response{Version: protocolVersion, ID: r.ID}
	var err error
	switch r.Method {
	case "query", "explain":
		if r.Method == "explain" {
			r.Query.Explain = true
		}
		resp.Result, err = e.Query(ctx, r.Query)
	case "ingest":
		err = e.Ingest(ctx, r.Events)
		resp.Result = map[string]bool{"ok": true}
	case "upsert":
		err = e.Upsert(ctx, r.Batch)
		resp.Result = map[string]bool{"ok": true}
	case "replay":
		err = e.Replay(ctx)
		resp.Result = map[string]bool{"ok": true}
	case "reindex":
		resp.Result, err = e.ReindexWithOptions(ctx, braid.ReindexOptions{Force: r.Force})
	case "reindex_start", "reindex_status", "reindex_cancel":
		if len(managers) == 0 {
			return fail(r.ID, "operation_failed", "background reindex requires a session")
		}
		return managers[0].handle(ctx, e, r)
	case "eval":
		resp.Result, err = e.Eval(ctx, r.Labels)
	case "dataset":
		resp.Result, err = e.Dataset(ctx)
	case "replace_snapshot":
		resp.Result, err = e.ReplaceSnapshot(ctx, r.Snapshot)
	case "apply":
		resp.Result, err = e.Apply(ctx, r.Update)
	}
	if err != nil {
		code := "operation_failed"
		if errors.Is(err, braid.ErrRevisionConflict) {
			code = "revision_conflict"
		}
		if errors.Is(err, braid.ErrDatasetMismatch) {
			code = "dataset_mismatch"
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "cancelled"
		}
		return fail(r.ID, code, err.Error())
	}
	return resp
}

// Drain oversized lines without allocating beyond the advertised limit, then
// resume at the next request. The limit excludes the newline delimiter.
func protocolLine(r *bufio.Reader) ([]byte, bool, error) {
	var b []byte
	oversize := false
	for {
		part, err := r.ReadSlice('\n')
		if len(part) > 0 && part[len(part)-1] == '\n' {
			part = part[:len(part)-1]
		}
		if len(b)+len(part) > maxRequestBytes {
			oversize = true
			b = nil
		}
		if !oversize {
			b = append(b, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && (len(b) > 0 || oversize) {
			err = nil
		}
		return b, oversize, err
	}
}
func serve(ctx context.Context, e *braid.Engine, in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	ready := false
	jobs := &reindexJobs{}
	defer jobs.close()
	for {
		b, tooLarge, err := protocolLine(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		resp := response{}
		if tooLarge {
			resp = fail(nil, "request_too_large", "request exceeds 16 MiB")
		} else {
			resp = handleRequest(ctx, e, b, &ready, jobs)
		}
		if err = enc.Encode(resp); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
}
