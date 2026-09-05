# Reusing embeddings and building without blocking readers

`Reindex(ctx)` now reuses validated vectors for unchanged nodes in the configured model. `ReindexWithOptions(ctx, ReindexOptions{Force: true})` explicitly regenerates everything. The richer call returns `{dataset, reused, generated}`. CLI `reindex` returns that report; add `--force` to regenerate all vectors.

The build captures node text, matching cached vectors and dataset revision in a read transaction. It releases the Engine lock before provider calls and holds generated vectors in an unpublished in-memory batch. On completion it reacquires the write lock, checks that the dataset state is unchanged, and atomically publishes the vectors and new revision. A concurrent content update yields `revision_conflict`; cancellation, provider errors, invalid vectors or dimension disagreement leave the previous publication intact. There is no automatic retry on a newer snapshot.

Reuse requires the configured model, a valid nonzero finite vector and consistent dimensions. Public text updates invalidate cached vectors; deleted nodes cannot keep them. Other model spaces are never reused. If a provider changes behind an unchanged model ID, Braid cannot infer that change: use a new immutable model ID or explicit force rebuild. Cached dimension disagreements require force/new model; a failed mixed-dimension build is never published.

Readers on the same Engine can query while provider generation runs. The initial capture and final database publication still briefly hold the Engine lock; publication includes table writes/content hashing and can take longer on a large corpus. Memory scales with the vector set. Reindex is not a durable build queue: a process crash discards the unpublished build. Custom embedding Providers must honor cancellation and support concurrent calls from query and reindex.

## Subprocess background jobs

The synchronous `reindex` request still waits for completion, occupying the sequential request loop. To keep the same subprocess available for queries, negotiate `reindex_start`, `reindex_status`, and `reindex_cancel` and use:

```json
{"id":"start","method":"reindex_start","force":false}
{"id":"status","method":"reindex_status","job_id":"reindex:1"}
{"id":"cancel","method":"reindex_cancel","job_id":"reindex:1"}
```

Start returns a job ID and state. Only one background job may run per session; another start returns `job_running`. Status is `running`, `succeeded`, `failed`, or `cancelled`; terminal success includes the reindex report, and terminal failure includes structured `{code,message}`. Cancellation requests cancellation and may initially still report running. If publication already completed, cancelling does not undo it.

Only the latest session job is retained; old/unknown IDs return `unknown_job`. Session close cancels the current build. Clients must keep the connection open until terminal status if they want completion. Background workers never write unsolicited stdout messages. There is no cross-process job persistence. Heimdall should record the returned dataset state and reconcile revision conflicts against its current source snapshot before deciding to rebuild.
