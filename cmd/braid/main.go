package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/algorhythmic/braid/pkg/braid"
	"gopkg.in/yaml.v3"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "braid:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: braid init|ingest|upsert|replay|query|explain|eval|reindex|serve [--config braid.yaml]")
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := fs.String("config", "braid.yaml", "YAML config path")
	input := fs.String("input", "-", "JSONL input file or - for stdin")
	query := fs.String("query", "", "query JSON or @file")
	labels := fs.String("labels", "", "label JSONL file")
	gridPath := fs.String("grid", "", "evaluation grid YAML file")
	proposal := fs.String("proposal", "braid.proposed.yaml", "new proposal YAML path (never overwritten)")
	objective := fs.String("metric", "ndcg_at_10", "grid objective: ndcg_at_10, recall_at_5, recall_at_20, mrr")
	stdio := fs.Bool("stdio", false, "serve newline-delimited JSON on stdin/stdout")
	force := fs.Bool("force", false, "regenerate all vectors during reindex")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if command == "init" {
		c := braid.DefaultConfig()
		b, err := yaml.Marshal(c)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		e, err := braid.Open(ctx, c)
		if err != nil {
			return err
		}
		defer e.Close()
		return json.NewEncoder(out).Encode(map[string]any{"config": *configPath, "database": c.Store.Path})
	}
	c, err := braid.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	e, err := braid.Open(ctx, c)
	if err != nil {
		return err
	}
	defer e.Close()
	enc := json.NewEncoder(out)
	switch command {
	case "dataset":
		state, err := e.Dataset(ctx)
		if err != nil {
			return err
		}
		return enc.Encode(state)
	case "replace-snapshot", "apply":
		r, close, err := reader(*input, in)
		if err != nil {
			return err
		}
		defer close()
		if command == "replace-snapshot" {
			values, err := decodeLines[braid.Snapshot](r)
			if err != nil {
				return err
			}
			if len(values) != 1 {
				return errors.New("replace-snapshot requires one JSON object")
			}
			state, err := e.ReplaceSnapshot(ctx, values[0])
			if err != nil {
				return err
			}
			return enc.Encode(state)
		}
		values, err := decodeLines[braid.Update](r)
		if err != nil {
			return err
		}
		if len(values) != 1 {
			return errors.New("apply requires one JSON object")
		}
		state, err := e.Apply(ctx, values[0])
		if err != nil {
			return err
		}
		return enc.Encode(state)
	case "ingest", "upsert":
		r, close, err := reader(*input, in)
		if err != nil {
			return err
		}
		defer close()
		if command == "ingest" {
			events, er := decodeLines[braid.Event](r)
			if er != nil {
				return er
			}
			if er = e.Ingest(ctx, events); er != nil {
				return er
			}
			return enc.Encode(map[string]any{"ingested": len(events)})
		}
		batches, err := decodeLines[braid.Batch](r)
		if err != nil {
			return err
		}
		b := braid.Batch{}
		for _, part := range batches {
			b.Nodes = append(b.Nodes, part.Nodes...)
			b.Edges = append(b.Edges, part.Edges...)
			b.Embeddings = append(b.Embeddings, part.Embeddings...)
		}
		if err = e.Upsert(ctx, b); err != nil {
			return err
		}
		return enc.Encode(map[string]any{"nodes": len(b.Nodes), "edges": len(b.Edges), "embeddings": len(b.Embeddings)})
	case "replay":
		if err = e.Replay(ctx); err != nil {
			return err
		}
		return enc.Encode(map[string]bool{"replayed": true})
	case "reindex":
		report, err := e.ReindexWithOptions(ctx, braid.ReindexOptions{Force: *force})
		if err != nil {
			return err
		}
		return enc.Encode(report)
	case "query", "explain":
		var q braid.Query
		if *query != "" {
			b := []byte(*query)
			if b[0] == '@' {
				b, err = os.ReadFile(string(b[1:]))
				if err != nil {
					return err
				}
			}
			if err = json.Unmarshal(b, &q); err != nil {
				return err
			}
		} else {
			if err = json.NewDecoder(in).Decode(&q); err != nil {
				return err
			}
		}
		if command == "explain" {
			q.Explain = true
		}
		res, err := e.Query(ctx, q)
		if err != nil {
			return err
		}
		return enc.Encode(res)
	case "eval":
		if *labels == "" {
			return errors.New("eval requires --labels FILE")
		}
		r, close, err := reader(*labels, in)
		if err != nil {
			return err
		}
		defer close()
		ls, err := decodeLines[braid.Label](r)
		if err != nil {
			return err
		}
		if *gridPath != "" {
			g, err := braid.LoadGrid(*gridPath)
			if err != nil {
				return err
			}
			// Refuse an existing destination before running an expensive search.
			if _, err = os.Stat(*proposal); err == nil {
				return fmt.Errorf("proposal path already exists: %s", *proposal)
			} else if !os.IsNotExist(err) {
				return err
			}
			report, err := e.GridSearch(ctx, ls, g, *objective)
			if err != nil {
				return err
			}
			if report.BestCondition == "" {
				return enc.Encode(report)
			}
			b, err := yaml.Marshal(report.Proposal)
			if err != nil {
				return err
			}
			f, err := os.OpenFile(*proposal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				return err
			}
			_, writeErr := f.Write(b)
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
			return enc.Encode(report)
		}
		res, err := e.Eval(ctx, ls)
		if err != nil {
			return err
		}
		return enc.Encode(res)
	case "serve":
		if !*stdio {
			return errors.New("serve requires --stdio")
		}
		return serve(ctx, e, in, out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
func reader(path string, in io.Reader) (io.Reader, func(), error) {
	if path == "-" {
		return in, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}
func decodeLines[T any](r io.Reader) ([]T, error) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 16<<20)
	out := []T{}
	line := 0
	for s.Scan() {
		line++
		if len(s.Bytes()) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(s.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, v)
	}
	return out, s.Err()
}
