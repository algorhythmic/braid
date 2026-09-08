# Implementation decisions and remaining work

The supplied `fuse-retrieval-engine.md` is design input, not authority to access consumer repositories or run their jobs. The implementation uses **Braid**, `braid`, and `.braid.db`. No upstream project or trademark availability check has been performed.

## First milestone

Implemented: SQLite/WAL and FTS5; transactional event projection and replay; lexical BM25, brute-force cosine and anchor centroids, anchor-seeded PageRank, recency; weighted RRF, boosts, atomic budgeted MMR selection; CLI and Go library; mandatory explanations. The subprocess interface, explicit embedding reindex, custom cost function, and initial metric harness are also implemented.

Only direct dependencies are `modernc.org/sqlite` and `gopkg.in/yaml.v3`, as requested by the draft. Their transitive dependencies are recorded in `go.mod` and `go.sum`. No external services are needed when embeddings are disabled.

## Resolved ambiguities

- **Graph direction:** the draft says undirected by default but only shows an `undirected` list. Braid follows that default and adds `graph.directed: [type, ...]` for exceptions. A type cannot appear in both lists. PageRank uses 20 iterations with restart-distributed dangling mass. Traversal uses positive edges and existing endpoints. Missing endpoint edges are retained for later node arrival. Explanations show deterministic shortest paths, not a claimed decomposition of PageRank mass.
- **Recency:** `half_life_hours` uses `exp(-ln(2) * age / half_life)`. The draft's `exp(-age / half_life)` would yield 0.368 instead of 0.5 after one half-life. Future timestamps receive age zero. Optional `temporal.neighborhood: true` restricts recency to the graph neighborhood when anchors are supplied.
- **Diversity during packing:** ranking version `mmr-admitted-dependencies-v2` selects complete, affordable dependency bundles using MMR against context actually admitted. There is no later score/cost sort. Similarity is nonnegative cosine when both vectors exist, otherwise token-set Jaccard. Mandatory/shared context, zero-cost cycles, and deterministic tie-breaking are supported. Reevaluate older grid proposals after this ranking change.
- **Budget:** zero is valid; negative/non-finite costs are rejected. Existing custom/node/attribute/text cost precedence is retained. Fixed overhead, content and required costs are reported separately. Projected context export adds an exact JSON byte limit covering selected metadata/explanations; its diagnostic envelope is separate. See the [retrieval contract](retrieval-contract.md) for complete semantics.

- **Projection fan-out:** edge `from` renders against the event payload; edge `to` renders against each item. `$event` always refers to the payload. Node rules can also use `each`. Event timestamp is the fallback for node timestamps and every projected edge. Missing text fields are omitted; missing template or `each` fields are errors. Boost predicates are intentionally restricted to `attrs.FIELD == VALUE`, not a general expression language.
- **Replay and direct writes:** legacy event stores retain full replay semantics, including removing unlogged upserts. New snapshot-managed datasets support atomic replace/apply/deletions and reject ingest/replay. Snapshot APIs reject databases with event logs, preventing mixed authority. Embeddings survive legacy replay only when final node ID and text match. Failed writes leave prior content/revision intact. See [dataset semantics](datasets.md).
- **Embeddings:** text changes invalidate cached vectors. Explicit reindex replaces all spaces transactionally after successful generation. A query that loads vectors rejects a store containing another model's vectors; it must reindex. Lexical queries that do not need vector diversity skip this check and all vector reads. Dense is disabled by `provider: none`, except when the library explicitly supplies a provider. Ollama is supported; the draft's Anthropic provider is not implemented. External embedding services may be nondeterministic; replay itself does not call them.
- **Reproducibility:** stable ID tie-breaking, sorted graph accumulation, deterministic projections, a pinned query `at`, and preserved vectors make ordinary replay results byte-identical in the fixture. Optional stage traces contain measured durations and are not byte-stable. `config_hash` identifies engine configuration, including paths. It is not a data snapshot, embedding implementation identity, or a hash of query overrides. No historical replay cutoff exists yet. Consumers must save the query and log boundary alongside the config hash for historical evaluations.
- **Concurrency:** one Engine serializes operations; each query/raw retrieval and each full eval/grid run pins a SQLite read snapshot. Other handles may publish while readers retain their older consistent view. Snapshot mutations acquire the write lock before checking expected revision. SQLite has a five-second busy timeout; callers must handle conflicts/busy failures. Direct external SQL edits bypass the dataset/revision contract.

## Evaluation available now

JSONL labels contain `query_id`, `query`, and expected IDs or explicit `no_answer`. Eval reports answerable-only ranking metrics plus coverage, abstention, no-answer false positives, precision and annotated stale/cross-workstream rates. Raw retriever/RRF conditions evaluate rankings; Braid evaluates packed output after optional evidence/min-score policy. A disabled dense retriever yields no hits. Metrics and full labels persist in SQLite. See [negative evaluation](negative-evaluation.md).

## Remaining milestones

1. Heimdall is the active integration. Follow its [adapter contract](heimdall-integration.md), isolated generation approach, and held-out task-assignment evaluation. Saga is deferred by user direction.
2. Config grid search and separate proposal output are now implemented; see [usage](grid-search.md). Projected assembled-context export is implemented; `eval record` for consumer outcomes remains unfinished.
3. All six [post-grid issues](roadmap.md) have implemented mechanisms and synthetic/contract tests, including embedding reuse/background builds and negative evaluations. Real Heimdall calibration, adapter deployment and scale measurements remain pending. Continue using one permitted dataset per database. Saga's exporter and completed-task ablations wait until its integration resumes.
4. Nexus scale work: storage backend interface/Postgres, vector index interface/ANN, scheduled projection jobs, and scheduled consumer ingestion. Typed metadata filters are implemented; these storage/index/job extensions are not implemented or claimed as stubs.
5. Learned fusion and cross-encoder reranking remain gated on labels and measurable improvement. No fabricated benchmark result is used to select graph damping or seed mode.

The first implementation does not establish that fusion beats dense-only or no context. That remains the empirical question for the consumer harness.

The [retrieval contract](retrieval-contract.md) documents hard eligibility, exact reads, revision preconditions, required context, conditional data loading, deadlines, and constrained evaluation.
