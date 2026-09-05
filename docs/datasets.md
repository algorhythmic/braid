# Dataset binding and snapshot updates

Set `dataset_id` in config to the account/project scope that the database is allowed to contain:

```yaml
dataset_id: heimdall:account-1:project-1
store: {backend: sqlite, path: ./project-1.braid.db}
```

**One database contains one dataset.** Its identity is persisted and immutable through the public API. Opening that file under another dataset ID fails. Multiple datasets in one file are unsupported; continue using separate files/processes for different permitted scopes. This is a boundary on a trusted bound Engine, not authentication or protection against callers who can read/edit SQLite files directly. The consumer remains responsible for putting only permitted content into each dataset.

Queries may supply `dataset_id`; a mismatch is rejected. Omitting it selects the Engine's already-bound dataset, never all datasets. Anchors, graph neighbors, FTS corpus statistics, vectors, and temporal candidates are all confined to that file. IDs are local to the dataset; identical IDs in two datasets identify different entities. A node ID is not a cross-dataset reference.

The default ID is `default` for compatibility. A populated legacy file without metadata can bind only to `default`; moving to a meaningful named scope requires rebuilding a fresh file from permitted source data. Braid never silently relabels legacy content. New integrations should always configure an explicit scope ID.

## Published identity

`Dataset(ctx)` / CLI `dataset` / subprocess `dataset` return `{id, revision, digest}`. Query results contain the same object as `dataset`, and hello reports the bound dataset. Evaluation/grid reports and persisted metrics also record it.

The revision starts at zero and increases when published node, edge, or vector content changes. A deterministic SHA-256 digest covers those tables in stable order. Idempotent writes and identical replay preserve the revision/digest; changing content back to an earlier value increases revision again. Existing populated legacy databases begin at revision zero when first bound. Event-log-only changes and evaluation records do not affect the published-content revision.

Every query/raw retriever runs in a SQLite read transaction. Evaluation and grid search pin one read transaction across all conditions. Dataset state and retrieved content therefore describe the same snapshot even when another Engine publishes concurrently. Config/algorithm versions are separate identities: continue recording config hash and the negotiated ranking version.

## Full snapshot replacement

Subprocess method `replace_snapshot`, library `ReplaceSnapshot(ctx, Snapshot)`, or CLI `replace-snapshot --input snapshot.json`:

```json
{"id":"publish","method":"replace_snapshot","snapshot":{"dataset_id":"heimdall:account-1:project-1","expected_revision":0,"batch":{"nodes":[{"id":"task:1","type":"task","text":"Repair queue","ts":"2026-09-04T12:00:00Z"}],"edges":[],"embeddings":[]}}}
```

This replaces the entire published node/edge/vector set in one transaction. Omitted nodes and edges are removed. Omitted embeddings are removed too; use incremental `apply` to preserve unchanged cached vectors. All supplied edge endpoints must exist in the resulting snapshot, and embedding references must be valid. Any failure rolls back all deletions and writes. Success returns the new dataset state directly as `result`.

## Revision-checked updates and deletion

Subprocess `apply`, library `Apply(ctx, Update)`, or CLI `apply --input update.json`:

```json
{"id":"update","method":"apply","update":{"dataset_id":"heimdall:account-1:project-1","expected_revision":1,"delete_nodes":["task:old"],"delete_edges":[{"src":"task:1","dst":"file:1","type":"about"}],"upsert":{"nodes":[],"edges":[],"embeddings":[]}}}
```

Order is node deletion (including incident edges and embeddings), explicit edge deletion, then upsert. FTS triggers remove deleted text. A deleted ID may be re-created in the same update, but its former edges/vectors do not return automatically. The final graph must have no missing endpoints. Deleting an absent node/edge is a no-op.

Both mutation APIs require exact `dataset_id` and a nonnegative `expected_revision`. Stale writers receive `revision_conflict`; wrong scope receives `dataset_mismatch`. The write lock is acquired before checking the revision. On timeout, read the current state and reconcile; do not assume the mutation failed or retry with a new revision blindly.

Snapshot APIs reject databases with existing event logs. After first snapshot/apply publication, ingest/replay are disabled for that database, preventing replay from resurrecting deleted content or clearing the snapshot. Existing unlogged `upsert` remains available and advances revisions but has no expected-revision check: adapters should use `apply` instead. Legacy event-backed datasets retain their ingest/replay semantics.

The subprocess's 16 MiB request limit still applies to full snapshots. Larger initial builds can use bounded apply requests in an unpublished database generation; publish that generation only when complete. There is no staged multi-request transaction API. Content hashing scans the published tables on each write. Reindex now reuses valid vectors and performs provider generation outside the Engine lock, with a revision check at publication; see [reindex](reindex.md). Large-corpus memory and publication latency still need measurement.
