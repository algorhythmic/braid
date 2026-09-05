# Evaluation grid search

```sh
braid eval --config examples/heimdall.yaml --labels heimdall-labels.jsonl --grid examples/grid.yaml --metric recall_at_5 --proposal heimdall.proposed.yaml
```

Populate the isolated Heimdall database through its snapshot adapter first. No real Heimdall corpus or labels are bundled. `examples/grid.yaml` contains the draft's 648 combinations. An optional `mmr_lambda: [0.5, 0.7, 1.0]` multiplies that count by three.

The CLI emits a JSON report to stdout and writes the winning configuration to a **new** proposal file. An existing proposal path, including the active config path, is rejected. Review the report and validate on separate held-out data before adopting a proposal. A best trial is always proposed, even if it ties or loses to the configured baseline; selection does not imply improvement.

The library entry point is `Engine.GridSearch(ctx, labels, grid, objective)`. It returns the report and configuration without writing a config file. Grid search is currently exposed through the library and CLI, not as an added subprocess method.

Supported axes are `weights` and `k` maps keyed by lexical/dense/graph/temporal, plus `mmr_lambda`, `graph.damping`, and `graph.seed`. Unknown fields/retrievers, empty axes, invalid values, and grids over 10,000 combinations are rejected. Omitted axes retain the base config. Labels with query-level weights are rejected to prevent them silently overriding grid parameters. Budgets, filters, anchors and query boosts remain part of each label.

Each trial evaluates the full Braid packed result order. Seven baseline conditions retain their existing semantics: raw independent retrievers, plain RRF, weighted RRF, and the configured full pipeline. Objectives are `ndcg_at_10` (default), `recall_at_5`, `recall_at_20`, or `mrr`. Exact ties select the first combination in stable axis order: lexical, dense, graph, temporal (weights then k for each), MMR, damping, seed; axis values preserve YAML list order.

Reports include every trial's metrics, effective config and config hash, the baseline metrics, objective, best condition, a run timestamp, and a hash of ordered labels with explicit recency clocks. They also record dataset ID, revision and content digest. One SQLite read snapshot covers every condition and trial. Missing query `at` values receive the same run timestamp, which is persisted with labels. Set `at` in every label to compare repeated runs deterministically. Continue recording the Heimdall generation manifest for source and adapter provenance.

No active configuration or node/edge/vector data is modified. Successful baseline and trial metrics persist to `eval_runs` together in one transaction. The engine lock covers the whole search; use a dedicated evaluation engine/database generation rather than a serving process. Embedding requests are memoized within the run so identical query text is embedded once. Other retrieval work is recomputed per trial; large grids can be expensive.

The harness supports mixed positive/no-answer labels and annotated negative metrics; ranking grid objectives require at least one answerable case. Review negative metrics alongside the ranking winner, since grid selection does not impose a false-positive constraint. See [metric definitions and optional abstention policy](negative-evaluation.md). MMR uses normalized relevance and a text-overlap fallback. Reevaluate old proposals after algorithm changes; config hashes alone do not encode code changes.
