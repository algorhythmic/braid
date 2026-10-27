package braid

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

// Package A: https://github.com/algorhythmic/muninn/blob/8bdda8b/docs/braid-integration/A-EMBEDDING-COMPATIBILITY.md
// Only sandbox port-bind permission denials justify leaving this fixture unrun.
func portBindPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
		strings.Contains(message, "operation not permitted") || strings.Contains(message, "permission denied")
}

func TestPortBindPermissionDenied(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"success", nil, false},
		{"eperm", syscall.EPERM, true},
		{"eacces", syscall.EACCES, true},
		{"wrapped-eperm", &net.OpError{Op: "listen", Net: "tcp", Err: fmt.Errorf("bind: %w", syscall.EPERM)}, true},
		{"wrapped-eacces", fmt.Errorf("listen: %w", syscall.EACCES), true},
		{"text-eperm", errors.New("listen tcp: operation not permitted"), true},
		{"text-eacces", errors.New("listen tcp: Permission Denied"), true},
		{"address-in-use", syscall.EADDRINUSE, false},
		{"address-unavailable", syscall.EADDRNOTAVAIL, false},
		{"other-failure", errors.New("listener setup failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := portBindPermissionDenied(tc.err); got != tc.want {
				t.Fatalf("portBindPermissionDenied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestOllamaContract(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if portBindPermissionDenied(err) {
			t.Skipf("sandbox port-bind denial: Ollama HTTP fixture is sandbox-unrun: %v", err)
		}
		t.Fatalf("create Ollama HTTP fixture listener: %v", err)
	}
	s := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})}}
	s.Start()
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
