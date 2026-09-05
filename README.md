# Braid

Explainable retrieval that fits a budget. Braid combines lexical search, embeddings, graph relationships and recency, then selects diverse context within a consumer-defined cost limit.

**Status:** early implementation with a Go library, CLI, versioned subprocess protocol and evaluation harness. Heimdall is the active integration; Saga is deferred. Synthetic and contract tests pass, but real consumer effectiveness and large-corpus performance have not been established.

## Features

- Independent SQLite FTS5/BM25, brute-force cosine, anchor-seeded PageRank and recency retrieval.
- Weighted reciprocal rank fusion, normalized MMR, and greedy budget packing, with explanations for every selected item.
- Text-only, anchor-only and combined queries; optional evidence gates and minimum fused scores.
- One dataset per SQLite database, with immutable identity, transactional snapshots, revision-checked updates and node/edge deletion.
- Validated embedding reuse and background reindex jobs with cancellation and stale-build protection.
- Per-retriever and fused evaluation, grid search, explicit no-answer cases and annotated negative metrics.

Node and edge types are consumer-defined labels. Budgets can represent estimated tokens, item counts, audio duration or another nonnegative cost.

## Quick start

Requires **Go 1.24+**. SQLite runs in-process; no C compiler or embedding service is needed for the example.

```sh
go test ./...
go build -o bin/braid ./cmd/braid

go run ./cmd/braid ingest --config examples/saga.yaml --input examples/events.jsonl
go run ./cmd/braid explain --config examples/saga.yaml --query @examples/query.json
go run ./cmd/braid replay --config examples/saga.yaml
go run ./cmd/braid eval --config examples/saga.yaml --labels examples/labels.jsonl
```

The Saga-named fixture is synthetic: four events and one label exercise ingestion, retrieval, replay and evaluation. It is not a Saga integration or a quality benchmark. Its pinned query time produces identical results after replay.

On Windows, build with `go build -o bin/braid.exe ./cmd/braid`. The optional `scripts/dev.ps1` wrapper uses `.tools/go` when present, otherwise Go from PATH. It does not install Go.

## Heimdall integration

Start from [examples/heimdall.yaml](examples/heimdall.yaml). Set an explicit `dataset_id` for the permitted account/project scope and use a separate database for each scope. Populate it from Heimdall's current source snapshot using `replace_snapshot` or revision-checked `apply`.

Requests for a different dataset fail. Queries and complete evaluation runs read a consistent SQLite snapshot and report dataset ID, revision and digest. Shared multi-dataset files are unsupported; the consumer must select permitted content before indexing it.

See the [integration guide](docs/heimdall-integration.md) for publication, supervision and assignment constraints.

## Interfaces

| Interface | Entry points |
|---|---|
| Go library | `Open`, `Query`, `Retriever`, `Ingest`, `Upsert`, `Replay`, `Dataset`, `ReplaceSnapshot`, `Apply`, `ReindexWithOptions`, `Eval`, `GridSearch` |
| CLI | `init`, `ingest`, `upsert`, `replay`, `query`, `explain`, `dataset`, `replace-snapshot`, `apply`, `reindex`, `eval`, `serve --stdio` |
| Subprocess | Negotiated version 1, newline-delimited JSON, strict validation, structured errors and background reindex jobs |

`braid init --config braid.yaml` creates a default config and database without overwriting an existing config. Paths in YAML resolve relative to the process working directory.

### Subprocess

Start `braid serve --stdio --config examples/heimdall.yaml`, then negotiate before querying or mutating data:

```json
{"id":"hello","method":"hello","versions":[1],"required_capabilities":["query","dataset_binding","replace_snapshot","apply","strict_requests"]}
{"id":"query-1","method":"query","query":{"text":"connection reset","filters":{"types":["task"]},"budget":{"max":500},"require_evidence":true}}
```

Responses contain `protocol_version`, the validated request ID, and either `result` or `error: {code, message}`. Every selected item includes text, score, cost and `why`. `explain: true` adds raw rank lists and fused order. The request limit is 16 MiB; stdout is reserved for protocol responses.

See the [protocol](docs/protocol.md) and [dataset mutation examples](docs/datasets.md).

### Go library

Import `github.com/algorhythmic/braid/pkg/braid` from a Go caller:

```go
cfg := braid.DefaultConfig()
cfg.DatasetID = "consumer:project-1"
cfg.Store.Path = "project-1.braid.db"

engine, err := braid.Open(ctx, cfg)
if err != nil {
    return err
}
defer engine.Close()

result, err := engine.Query(ctx, braid.Query{
    Text:            "connection reset",
    Anchors:         []string{"task:1"},
    Budget:          &braid.Budget{Max: 500},
    RequireEvidence: true,
})
```

`WithProvider` supplies a custom embedding provider; `WithCost` supplies a custom cost function. Providers must support cancellation and concurrent calls.

## Embeddings

Dense retrieval is disabled by default. To use a model already available in Ollama:

```yaml
embed:
  provider: ollama
  model: nomic-embed-text
  url: http://localhost:11434
```

Run `braid reindex --config YOUR_CONFIG`. Ingestion does not call the provider or download models. Reindex reuses valid vectors for unchanged text/model; `--force` regenerates all. Failed, cancelled or stale builds do not publish. Provider generation runs outside the Engine lock; capture and publication still take locks.

Use `reindex_start`, `reindex_status` and `reindex_cancel` to keep a subprocess available during builds. See [reindex lifecycle](docs/reindex.md). The Ollama adapter implements [`POST /api/embed`](https://docs.ollama.com/api/embed).

## Evaluation

```sh
go run ./cmd/braid eval --config examples/saga.yaml --labels examples/labels.jsonl --grid examples/grid.yaml --metric recall_at_5 --proposal braid.proposed.yaml
```

The supplied grid has 648 combinations across weights, RRF k, graph damping and seed; MMR lambda is also supported. All trials use one dataset snapshot. The winning config is proposed separately and never applied automatically. Existing proposal files are not overwritten.

Reports include answerable-only recall/MRR/nDCG, coverage, abstention, precision, no-answer false positives and annotated stale/cross-workstream hits. Evidence gates and minimum fused scores are evaluation controls, not calibrated confidence probabilities.

See [grid search](docs/grid-search.md) and [negative evaluation](docs/negative-evaluation.md) for labels, metric definitions and baseline semantics. Validate on held-out data before adopting settings.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/braid
```

CI is configured for Linux and Windows. The acceptance fixture generates 200 nodes, 600 edges and 40 labels and compares replay output with all four retrievers. Additional tests cover scope checks, atomic rollback, revision conflicts, deletion cleanup, reader snapshots, diversity, protocol validation, background jobs and negative metrics.

On Windows, `./scripts/verify-contract.ps1` exercises the subprocess against a fresh temporary database. No live Ollama instance is required for automated tests.

## Current limits

- Heimdall adapter deployment and held-out calibration remain pending; Saga integration is deferred.
- Dataset isolation is one database per scope, not authentication or shared-file tenancy.
- Snapshot-managed databases reject ingest/replay. Legacy replay removes unlogged direct upserts.
- Query `at` pins recency; it is not a historical data cutoff. Record the dataset revision and source boundary too.
- Dense search is brute force. Reindex builds are in memory; large-corpus memory and publication latency need measurement.
- Postgres, ANN, context export, consumer outcome recording and learned reranking are not implemented.

See [implementation decisions](docs/implementation.md), [verification notes](docs/verification.md) and the [roadmap](docs/roadmap.md).
