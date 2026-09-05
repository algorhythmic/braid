# Current priorities

Updated from the user's direction: **Heimdall is the active integration; Saga is deferred.** The existing Saga example remains a synthetic engine smoke test.

## Evaluation grid search

Implemented: Cartesian search over retriever weights and RRF k, MMR lambda, graph damping, and seed; independent baselines; full packed-output metrics per trial; configurable selection metric; a separate, loadable YAML proposal that never replaces the active config. See [grid search](grid-search.md).

Run on Heimdall's permitted snapshot and held-out labels before choosing defaults. Synthetic runs validate mechanics only. Current positive-label metrics cannot establish assignment precision or safe abstention.

## Follow-up issues after grid search

B-01 through B-06 are implemented with tests, with B-03 enforced by immutable database binding rather than multi-dataset files. Real Heimdall quality validation, threshold calibration and adapter deployment remain pending. Continue using one permitted dataset per database; shared multi-dataset indices remain unsupported.

| ID | Priority | Issue and required change | Acceptance evidence |
|---|---|---|---|
| B-01 | High | Implemented: normalize MMR relevance by maximum boosted score; use nonnegative cosine or token-set Jaccard overlap. No type-based redundancy. | Tests cover scale invariance, strong relevance with and without embeddings, duplicate penalties, and type invariance. Held-out comparison remains pending. |
| B-02 | High | Implemented: required version/capability negotiation, strict nested request validation, duplicate-key rejection, method-specific fields, and structured errors. | Tests cover negotiation, malformed requests, unknown fields, oversized-request recovery, and numeric ID preservation. Updated Heimdall contract checker passes; actual adapter adoption is still required. |
| B-03 | High for shared indices | Implemented: immutable dataset ID bound to each database, request scope checks, identity in results and pinned read transactions. Multiple datasets per file remain unsupported. | Tests reject rebinding, wrong-scope queries and foreign anchors/content across every retriever. Consumers must still materialize only permitted content and use separate files per scope. |
| B-04 | Medium–high | Implemented: transactional snapshot replacement and revision-checked updates/deletions, including edge removal, FTS/vector cleanup and dataset identity. | Tests cover stale writers, rollback, deletion, no-op revisions, snapshot-mode replay protection, and concurrent readers retaining a consistent older revision. |
| B-05 | Medium | Implemented: validated vector reuse, provider generation outside the Engine lock, revision-checked atomic publication, and session background start/status/cancel. | Tests cover reuse/force, concurrent queries and writes, stale-build rejection, cancellation preservation and responsive subprocess requests. Capture/publication still hold locks; builds are in memory, not durable jobs. |
| B-06 | Medium | Implemented: explicit no-answer labels, optional evidence/min-score policy, precision/coverage, no-answer false positives, and annotated stale/cross-workstream rates. | Synthetic eval/grid tests verify denominators, policy behavior, persisted labels and contradictory-label rejection. Held-out calibration remains pending. |

Heimdall's adapter design must adopt the [version 1 protocol](protocol.md), [dataset/snapshot APIs](datasets.md), [background reindex](reindex.md) and [negative evaluation](negative-evaluation.md). Retain the pinned binary and supervised processes. Separate database generations remain useful for large full-corpus builds. Heimdall still owns calibrated assignment decisions and acceptance-time validation.

Remaining broader work: context export and outcome recording, then real consumer benchmarks. Saga exporter and task ablations wait until Saga integration resumes. Postgres/ANN and learned reranking remain later work.
