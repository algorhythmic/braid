# Local verification

Verified on Windows with the official Go 1.27.1 archive, downloaded into `.tools` and checked against the SHA-256 from Go's release metadata. The module declares Go 1.24. The CI workflow is provided for Linux and Windows; remote CI has not been run.

- `go test ./...`: passes for library and CLI, including the generated 200-node / 600-edge / 40-label acceptance fixture.
- `go vet ./...`: passes.
- `go build -o bin/braid.exe ./cmd/braid`: succeeds.
- Example CLI: ingests four events, returns two reports totaling 66 cost within a 100 budget, and produces byte-identical explain output after replay.
- Example evaluation: all seven conditions execute and persist metrics. The one-label example is only a smoke test, not evidence of retrieval quality.

Dense retrieval is tested using deterministic injected vectors/providers and an HTTP test server that checks the Ollama request/response contract. No live Ollama instance or downloaded embedding model was used. The synthetic replay test covers all four retrievers, including cached embeddings surviving replay.

Actual consumer data, downstream model success, scaling at 20,000 vectors, and race-detector execution have not been verified in this pass.

## Grid-search follow-up

Tests, vet, and build pass after adding grid search. New tests cover Cartesian expansion and validation, winner selection, unchanged active configuration, strict grid YAML, memoized query embeddings, stable pinned-label results, persisted trials, and CLI proposal overwrite protection.

The complete 648-combination grid also ran successfully through the CLI on a private synthetic Heimdall-shaped two-task index: seven baselines, 648 trials, and a separately written YAML proposal. This validates the grid mechanics, not parameter quality or Heimdall assignment performance. Heimdall is the active integration; the Saga fixture remains only a synthetic smoke test.

## Diversity and protocol follow-up

`go test ./...`, `go vet ./...`, and the Windows binary build pass after normalized MMR and protocol version 1 were added. New tests cover relevance scale invariance with/without vectors, text overlap and empty-text behavior, type-independent redundancy, negotiation/capability failures, strict nested fields, duplicate keys, null payloads, exact numeric IDs and oversized-line recovery.

The updated Heimdall subprocess checker also passes: version/capability negotiation, upsert, structured parse errors, unknown nested filter rejection, recovery, explanations, explicit costs, repeatable output and the replay/direct-upsert caveat. No actual Heimdall daemon or adapter deployment was performed, and no new held-out quality result is claimed.

## Dataset and snapshot follow-up

Tests, vet and the binary build pass with immutable dataset binding, query/evaluation read snapshots, revision-checked replacement/update/deletion, and content identity. Tests cover wrong-scope access through every retriever, legacy rebinding rejection, stale writes across handles, rollback, deleted FTS/vector/edge cleanup, snapshot-mode replay protection and a reader retaining its old revision during publication from another Engine.

The Heimdall executable checker now also passes dataset binding, snapshot publication, stale revision rejection and transactional deletion. Multi-dataset files are unsupported, and real consumer deployment/held-out evaluation remain pending.

## Reindex and negative-evaluation follow-up

Tests, vet and build pass. Added tests cover vector reuse/force, query/write responsiveness while generation is blocked, stale-build rejection, cancellation preserving vectors, background job requests, explicit no-answer semantics, evidence/min-score abstention, metric denominators, mixed-label grids, and full label persistence. The existing Heimdall executable checker still passes. Providers in concurrency tests are deterministic injected implementations; no live Ollama or real Heimdall calibration is claimed.
