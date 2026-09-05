# Subprocess protocol version 1

This replaces the initial unversioned protocol. Every connection must negotiate before querying or mutating data. There is no silent legacy mode. Heimdall must update its adapter and pin the rebuilt executable together.

Send one UTF-8 JSON object per line. The first successful request must be:

```json
{"id":"hello","method":"hello","versions":[1],"required_capabilities":["query","upsert","strict_requests","structured_errors"]}
```

The response carries `protocol_version: 1` at the top level and a `result` object with `protocol_version`, `capabilities`, `max_request_bytes`, and `ranking_version`. The ranking version for the revised MMR implementation is `mmr-normalized-jaccard-v1`. Record it with the binary fingerprint and dataset generation. A config hash alone does not identify algorithm changes.

Supported capabilities are `query`, `explain`, `upsert`, `ingest`, `replay`, `reindex`, `eval`, `strict_requests`, `structured_errors`, `dataset_binding`, `dataset`, `replace_snapshot`, `apply`, `reindex_start`, `reindex_status`, `reindex_cancel`, `embedding_reuse`, `negative_eval`, and `abstention_policy`. Method capability means implemented, not necessarily configured: reindex needs a provider and ingest/replay are disabled for snapshot-managed databases. Hello also reports the bound `dataset` identity. Dataset binding means one dataset per database, not shared-file tenancy.

After successful negotiation, requests use the same method payloads as before. Version is selected for the connection; do not add a version field to ordinary requests. A repeated hello returns `already_negotiated`. Failed negotiation leaves the connection unnegotiated and allows a corrected hello.

| Method | Required payload | Optional payload |
|---|---|---|
| hello | `versions`: nonempty integer array | `required_capabilities`: string array |
| query / explain | `query`: object, including `{}` | — |
| upsert | `batch`: object | — |
| ingest | `events`: array | — |
| eval | `labels`: array | — |
| replay | — | — |
| reindex / reindex_start | — | `force`: boolean |
| reindex_status / reindex_cancel | `job_id`: string | — |
| dataset | — | — |
| replace_snapshot | `snapshot`: object | — |
| apply | `update`: object | — |

Every request requires `id` (nonempty string or JSON number) and `method`. Numeric IDs are preserved without floating-point conversion. Unknown fields, case variants, duplicate object keys, wrong value types, null required payloads, and fields belonging to another method are rejected. Nested query/batch/event/label schemas are checked. Consumer-owned attrs and event payload keys remain arbitrary; duplicate keys are rejected even there. These strict wire rules do not change the separate CLI file-input format.

Responses have exactly one of `result` or `error`. Errors are objects:

```json
{"protocol_version":1,"id":"query-1","error":{"code":"negotiation_required","message":"send hello with supported versions before other methods"}}
```

Codes: `parse_error`, `invalid_request`, `unknown_method`, `negotiation_required`, `version_not_supported`, `capability_not_supported`, `already_negotiated`, `request_too_large`, `dataset_mismatch`, `revision_conflict`, `operation_failed`, and `cancelled`. Inspect codes rather than parsing messages. Other engine semantic validation, provider and storage failures use `operation_failed`; this code does not authorize blind mutation retries. IDs are echoed when validated. Parse/schema failures may omit the ID because the envelope cannot be trusted; treat those as protocol faults in the adapter. See [dataset APIs](datasets.md) for snapshot/update payloads and revision semantics.

Maximum request size is 16 MiB excluding the newline. Oversized lines are drained with bounded buffering, receive a structured error, and the next line can be processed. Malformed requests do not terminate the process. The request loop remains sequential, but `reindex_start` creates a cancellable background build so subsequent queries can proceed. Job-specific errors add `job_running` and `unknown_job`. Synchronous `reindex` returns the dataset/reused/generated report. See [job lifecycle and cancellation](reindex.md). Stdout is protocol only, stderr diagnostics. Client deadlines, response size limits, process supervision and safe mutation recovery remain Heimdall's responsibility.
