package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/algorhythmic/braid/pkg/braid"
	"gopkg.in/yaml.v3"
)

func TestCLIGridProposalDoesNotOverwriteConfig(t *testing.T) {
	dir := t.TempDir()
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(dir, "index.db")
	b, _ := yaml.Marshal(c)
	config := filepath.Join(dir, "config.yaml")
	grid := filepath.Join(dir, "grid.yaml")
	labels := filepath.Join(dir, "labels.jsonl")
	proposal := filepath.Join(dir, "proposed.yaml")
	for path, data := range map[string][]byte{config: b, grid: []byte("graph.damping: [0.5, 0.85]\n"), labels: []byte(`{"query_id":"q","query":{"at":"2026-09-04T12:00:00Z"},"expected":["a"]}`)} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"eval", "--config", config, "--labels", labels, "--grid", grid, "--proposal", config}
	var out bytes.Buffer
	if err := run(context.Background(), args, bytes.NewReader(nil), &out); err == nil {
		t.Fatal("allowed active config overwrite")
	}
	args[len(args)-1] = proposal
	if err := run(context.Background(), args, bytes.NewReader(nil), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := braid.LoadConfig(proposal); err != nil {
		t.Fatal("proposal is not loadable", err)
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(b, after) {
		t.Fatal("active config changed")
	}
	if err := run(context.Background(), args, bytes.NewReader(nil), &out); err == nil {
		t.Fatal("overwrote prior proposal")
	}
}

func TestCLIGridDoesNotWriteAnInfeasibleProposal(t *testing.T) {
	dir := t.TempDir()
	c := braid.DefaultConfig()
	c.Store.Path = filepath.Join(dir, "db")
	config := filepath.Join(dir, "config.yaml")
	grid := filepath.Join(dir, "grid.yaml")
	labels := filepath.Join(dir, "labels.jsonl")
	proposal := filepath.Join(dir, "proposal.yaml")
	b, _ := yaml.Marshal(c)
	for path, data := range map[string][]byte{config: b, grid: []byte("mmr_lambda: [1]\nconstraints:\n  max_missing_required_rate: 0\n"), labels: []byte(`{"query_id":"q","query":{"mandatory_ids":["missing"]},"expected":["action"]}`)} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"eval", "--config", config, "--grid", grid, "--labels", labels, "--proposal", proposal}, bytes.NewReader(nil), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(proposal); !os.IsNotExist(err) {
		t.Fatal("wrote an infeasible proposal", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"status":"no_feasible_configuration"`)) {
		t.Fatal(out.String())
	}
}
