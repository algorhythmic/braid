# Subprocess protocol version 1

This replaces the initial unversioned protocol. Every connection must negotiate before querying or mutating data. There is no silent legacy mode. Heimdall must update its adapter and pin the rebuilt executable together.

Send one UTF-8 JSON object per line. The first successful request must be:

```json
{"id":"hello","method":"hello","versions":[1],"required_capabilities":["query","upsert","strict_requests","structured_errors"]}
```

The response carries `protocol_version: 1` at the top level and a `result` object with `protocol_version`, `capabilities`, `max_request_bytes`, and `ranking_version`. The ranking version for the revised MMR implementation is `mmr-admitted-dependencies-v2`. Record it with the binary fingerprint and dataset generation. A config hash alone does not identify algorithm changes.

Supported capabilities are `query`, `explain`, `upsert`, `ingest`, `replay`, `reindex`, `eval`, `strict_requests`, `structured_errors`, `dataset_binding`, `dataset`, `replace_snapshot`, `apply`, `reindex_start`, `reindex_status`, `reindex_cancel`, `embedding_reuse`, `negative_eval`, `abstention_policy`, `hard_filters`, `allowed_ids`, `get_many`, `read_revision`, `required_context`, `context_export`, `query_timeout`, `stage_stats`, `constrained_eval`, and `query_embedding`. Method capability means implemented, not necessarily configured: reindex needs a provider and ingest/replay are disabled for snapshot-managed databases. Hello also reports the bound `dataset` identity. Dataset binding means one dataset per database, not shared-file tenancy.

After successful negotiation, requests use the same method payloads as before. Version is selected for the connection; do not add a version field to ordinary requests. A repeated hello returns `already_negotiated`. Failed negotiation leaves the connection unnegotiated and allows a corrected hello.

| Method | Required payload | Optional payload |
|---|---|---|
| hello | `versions`: nonempty integer array | `required_capabilities`: string array |
| query / explain | `query`: object, including `{}` | `timeout_ms`: integer |
| get_many | `get`: exact-read object | `timeout_ms`: integer |
| export_context | `export`: query and output projection object | `timeout_ms`: integer |
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

Codes: `parse_error`, `invalid_request`, `unknown_method`, `negotiation_required`, `version_not_supported`, `capability_not_supported`, `already_negotiated`, `request_too_large`, `dataset_mismatch`, `revision_conflict`, `operation_failed`, and `cancelled`. Inspect codes rather than parsing messages. Query embedding validation adds the codes in the [package A query embedding extension](#package-a-query-embedding-extension) below. Other engine semantic validation, provider and storage failures use `operation_failed`; this code does not authorize blind mutation retries. IDs are echoed when validated. Parse/schema failures may omit the ID because the envelope cannot be trusted; treat those as protocol faults in the adapter. See [dataset APIs](datasets.md) for snapshot/update payloads and revision semantics.

Maximum request size is 16 MiB excluding the newline. Oversized lines are drained with bounded buffering, receive a structured error, and the next line can be processed. Malformed requests do not terminate the process. The request loop remains sequential, but `reindex_start` creates a cancellable background build so subsequent queries can proceed. Job-specific errors add `job_running` and `unknown_job`. Synchronous `reindex` returns the dataset/reused/generated report. See [job lifecycle and cancellation](reindex.md). Stdout is protocol only, stderr diagnostics. Request-scoped `timeout_ms` is available for query/read/export methods; bounded context export limits the projected context array, not the diagnostic envelope. Client watchdogs, response transport limits, process supervision and safe mutation recovery remain the consumer's responsibility. See the [constrained retrieval contract](retrieval-contract.md) for payloads, filter semantics, required context and accounting.


## Package A query embedding extension

The optional protocol v1 capability `query_embedding` implements the generic API
extension for [package A](https://github.com/algorhythmic/muninn/blob/8bdda8b/docs/braid-integration/A-EMBEDDING-COMPATIBILITY.md).
Check the negotiated capability before sending it. The field belongs to the
query object: `query.query_embedding` for `query` and `explain`, and
`export.query.query_embedding` for `export_context`.

```json
{"id":"q1","method":"query","query":{"text":"synthetic example","query_embedding":{"model":"example-space-v1","vector":[0.12,-0.04]}}}
```

```json
{"id":"e1","method":"export_context","export":{"query":{"text":"synthetic example","query_embedding":{"model":"example-space-v1","vector":[0.12,-0.04]}},"output":{"fields":["id","text"],"max_bytes":4096}}}
```

The Go field is `Query.QueryEmbedding *QueryEmbedding`, whose value contains
`Model string` and `Vector []float32`. Omission or `null` means absent. A supplied
object must contain a nonempty `model` that exactly matches the dataset's
configured `embed.model`, and a nonempty, finite, nonzero `vector` with the stored
embedding dimension for that model. The model names the complete embedding
space; callers own compatibility of preprocessing and vector production.

A supplied vector is authoritative for dense retrieval, including fused queries
and raw dense retrieval: Braid does not call its embedding Provider. Text can
still drive lexical retrieval. Without a supplied vector, the existing dense
path embeds nonblank text through the configured provider, or averages stored
anchor vectors for blank text, subject to the existing provider configuration.
Supplying a vector and anchors with blank text is ambiguous and rejected;
nonblank text plus a vector and explicit anchors is allowed, with anchors
available to the graph stage.

Validation runs even when dense retrieval is disabled or filters leave no
eligible nodes. Dimension validation uses stored embeddings for the configured
model without applying query filters. If no stored dimension is available,
Braid rejects the supplied vector. The extension does not infer dimension from
the request itself.

Semantic failures return a Go `*braid.EmbeddingIncompatibilityError` and a
structured protocol error with the following `code`:

| Condition | Code |
|---|---|
| Empty or missing vector in a supplied object | `embedding_empty_vector` |
| All-zero vector | `embedding_zero_vector` |
| Nonfinite vector element through the Go API | `embedding_nonfinite_vector` |
| Empty model, empty configured model, or model mismatch | `embedding_model_mismatch` |
| Vector dimension differs from the stored model dimension | `embedding_dimension_mismatch` |
| Supplied vector with anchors and blank text | `embedding_ambiguous_anchors` |
| No stored dimension for the configured model | `embedding_dimension_unavailable` |

Strict decoding applies to the embedding object and vector elements. Unknown
fields, duplicate keys, case variants, wrong field types, nonnumeric elements,
and numbers that overflow `float32` return `invalid_request`. Invalid JSON,
including unquoted `NaN` or `Infinity` tokens, returns `parse_error`. JSON
`null` vector elements are invalid request values. Native Go callers can supply
NaN or infinity values, which receive `embedding_nonfinite_vector`.
