# Braid

Explainable retrieval that fits a budget. Braid combines lexical search, embeddings, graph relationships and recency, then selects diverse context within a consumer-defined cost limit.

**Status:** early implementation with a Go library, CLI, versioned subprocess protocol and evaluation harness. Heimdall is the active integration; Saga is deferred. Synthetic and contract tests pass. Warm lexical retrieval has been [benchmarked on 1,998 and 19,980 records](docs/retrieval-benchmarks.md); held-out consumer effectiveness and end-to-end deployment performance remain unverified.

## Features

- Independent SQLite FTS5/BM25, brute-force cosine, anchor-seeded PageRank and recency retrieval.
- Weighted reciprocal rank fusion and diversity-aware atomic dependency packing, with explanations for every selected item.
- Text-only, anchor-only and combined queries; optional evidence gates and minimum fused scores.
- Typed hard metadata filters and allowed IDs applied before candidate limits.
- Exact batch reads by ID, explicit missing IDs, and optional revision preconditions on reads and queries.
- Required dependencies and mandatory context selected as complete bundles; shared prerequisites are charged once.
- Projected JSON context export with an exact byte limit, cost accounting, optional stage traces, and subprocess request deadlines.
- One dataset per SQLite database, with immutable identity, transactional snapshots, revision-checked updates and node/edge deletion.
- Validated embedding reuse and background reindex jobs with cancellation and stale-build protection.
- Per-retriever and fused evaluation, grid search, explicit no-answer cases, and optional gates for forbidden results, missing required context, and budget overruns.

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

Development and deployment target Linux/Omarchy. The optional `scripts/dev.sh` wrapper uses `.tools/go/bin/go` when present, otherwise Go from PATH. It runs from the repository root, forwards all Go arguments, preserves explicit `GOPATH`/`GOCACHE` settings, and does not install Go.

## Heimdall integration

Start from [examples/heimdall.yaml](examples/heimdall.yaml). Set an explicit `dataset_id` for the permitted account/project scope and use a separate database for each scope. Populate it from Heimdall's current source snapshot using `replace_snapshot` or revision-checked `apply`.

Requests for a different dataset fail. Queries and complete evaluation runs read a consistent SQLite snapshot and report dataset ID, revision and digest. Shared multi-dataset files are unsupported; the consumer must select permitted content before indexing it.

See the [integration guide](docs/heimdall-integration.md) for publication, supervision and assignment constraints.

## Interfaces

| Interface | Entry points |
|---|---|
| Go library | `Open`, `Query`, `Retriever`, `GetMany`, `Export`, `Ingest`, `Upsert`, `Replay`, `Dataset`, `ReplaceSnapshot`, `Apply`, `ReindexWithOptions`, `Eval`, `GridSearch` |
| CLI | `init`, `ingest`, `upsert`, `replay`, `query`, `explain`, `dataset`, `replace-snapshot`, `apply`, `reindex`, `eval`, `serve --stdio` |
| Subprocess | Negotiated version 1, newline-delimited JSON; `query`/`explain`, `get_many`, `export_context`, dataset reads/mutations, evaluation, and background reindex jobs |

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

### Simple retrieval

For **known IDs**, use `Engine.GetMany(ctx, braid.GetRequest{...})` or subprocess `get_many`. It returns exact stored records in first-occurrence request order, deduplicates IDs, and reports `missing_ids` alongside the dataset identity. IDs preserve case and punctuation. Supply the revision from a prior dataset/query response when the reads must agree; the example below assumes revision 12:

```json
{"id":"hello","method":"hello","versions":[1],"required_capabilities":["get_many","read_revision"]}
{"id":"get-1","method":"get_many","get":{"dataset_id":"heimdall:assignment:example","ids":["task:1","task:2"],"expected_revision":12}}
```

An explicit matching `dataset_id` and an `ids` array are required. Omit `expected_revision` to read the current snapshot; a stale supplied revision returns `revision_conflict`. Exact lookup performs no fuzzy search or embedding calls.

For **keyword search**, the Go library exposes a raw BM25 list:

```go
lexical, err := engine.Retriever("lexical")
if err != nil {
    return err
}
hits, err := lexical.Retrieve(ctx, braid.Query{Text: "connection reset"}, 10)
```

This returns IDs, ranks, scores, and evidence directly from lexical retrieval. Use `GetMany` to hydrate the IDs; supply the same `expected_revision` to both operations when consistency is required.

The subprocess uses `query` for keyword search with budgeted item output. On a new connection, negotiate and explicitly disable the other retrieval stages:

```json
{"id":"hello","method":"hello","versions":[1],"required_capabilities":["query","query_timeout"]}
{"id":"search-1","method":"query","timeout_ms":250,"query":{"text":"connection reset","weights":{"lexical":1,"dense":0,"graph":0,"temporal":0},"budget":{"max":500}}}
```

Budgeted queries still apply diversity during selection. To avoid vector reads entirely, leave the embedding model unset or configure `defaults.mmr_lambda: 1`; disabling dense retrieval alone does not disable vector-based diversity.

For a compact model payload, `Engine.Export` / `export_context` projects selected fields and bounds the serialized `context` array in bytes. The diagnostic envelope is outside that limit. See the [retrieval contract](docs/retrieval-contract.md) for output examples, filters, required bundles, accounting, and deadlines. The current ranking version is `mmr-admitted-dependencies-v2`.

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

Optional grid constraints reject trials that exceed declared forbidden-hit, missing-required-context, or budget-overrun rates. If no trial passes, no proposal file is written. See [evaluation gates](docs/retrieval-contract.md#evaluation-gates).

See [grid search](docs/grid-search.md) and [negative evaluation](docs/negative-evaluation.md) for labels, metric definitions and baseline semantics. Validate on held-out data before adopting settings.

## Development

```sh
./scripts/dev.sh test -race ./...
./scripts/dev.sh vet ./...
./scripts/dev.sh build -o bin/braid ./cmd/braid
./scripts/verify-contract.sh
```

CI runs the Bash helpers on Linux, including the race-enabled Go suite and subprocess contract checker. The acceptance fixture generates 200 nodes, 600 edges and 40 labels and compares replay output with all four retrievers. Additional tests cover scope checks, atomic rollback, revision conflicts, deletion cleanup, reader snapshots, diversity, protocol validation, background jobs and negative metrics.

The contract checker needs Bash, Go, `jq`, and GNU `timeout`. It builds a temporary binary and exercises the subprocess against a fresh temporary database with a 30-second watchdog. Scratch files are retained at the printed path for inspection. No live Ollama instance is required for automated tests.

## Current limits

- Heimdall adapter deployment and held-out calibration remain pending; Saga integration is deferred.
- Dataset isolation is one database per scope, not authentication or shared-file tenancy.
- Snapshot-managed databases reject ingest/replay. Legacy replay removes unlogged direct upserts.
- Query `at` pins recency; it is not a historical data cutoff. Record the dataset revision and source boundary too.
- Dense search is brute force. Reindex builds are in memory; large-corpus memory and publication latency need measurement.
- Postgres, ANN, consumer outcome recording and learned reranking are not implemented.

See [implementation decisions](docs/implementation.md), [verification notes](docs/verification.md) and the [roadmap](docs/roadmap.md).
