package braid

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaContract(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != "POST" {
			t.Error("unexpected endpoint")
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req["model"] != "test" || req["truncate"] != false {
			t.Error(req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embeddings":[[1,0],[0,1]]}`))
	}))
	defer s.Close()
	p := Ollama{URL: s.URL, ModelName: "test"}
	vs, err := p.Embed(testCtx, []string{"a", "b"})
	if err != nil || len(vs) != 2 {
		t.Fatal(vs, err)
	}
	if _, err = p.Embed(testCtx, []string{"a"}); err == nil {
		t.Fatal("wrong vector count accepted")
	}
}
