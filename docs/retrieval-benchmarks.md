# Retrieval measurements — September 7, 2026

Warm Go-library lexical retrieval was measured against baseline commit `b530618`
and the accompanying retrieval-contract changes. This measures this query and
fixture, not end-to-end computer use or retrieval quality.

The source was the current ten generated wayland-computer-use catalogs: 1,998
binding records. Each record became a node with its binding fields plus `app` as
attributes, and available `command`, `description`, `action`, and `context`
fields concatenated as text. IDs were assigned deterministically. No app
configuration, source catalog, or adapter was modified. The local Batch JSON
SHA-256 was `8628e59b2ae27224810eb9dcebbb1f4904c01e3ddf9fe674b4393df49a774814`.
The 19,980-record fixture repeats this corpus ten times with distinct IDs.

Every fixture has synthetic ring edges and two-dimensional stored vectors so
unused reads remain measurable. The vectors are deliberately artificial and
provide **no evidence about semantic retrieval quality**. All nodes cost one;
budget is 20, candidate limit 200, query is `find files`, and only lexical
retrieval has nonzero weight. Lambda 1 needs no vector diversity; lambda 0.4
does. Provider calls, ingestion, and process startup are excluded.

Environment: Linux amd64, Intel Core i5-8600K at 3.60 GHz, Go 1.24.7, repository
SQLite dependency 1.38.2. Each case used three warmups and 100 timed queries.
Baseline and final benchmarks ran sequentially. These are single-run sample
percentiles on an active desktop, not confidence intervals or service SLOs.

| Rows | Lambda | Baseline p50 / p95 (ms) | Updated p50 / p95 (ms) | Baseline / updated bytes allocated per query | Baseline / updated allocations |
|---:|---:|---:|---:|---:|---:|
| 1,998 | 1.0 | 24.654 / 25.959 | 1.266 / 1.634 | 10,357,747 / 387,203 | 210,292 / 4,730 |
| 1,998 | 0.4 | 24.633 / 26.168 | 1.658 / 2.192 | 10,357,665 / 410,216 | 210,291 / 5,238 |
| 19,980 | 1.0 | 255.074 / 262.322 | 6.225 / 6.719 | 99,753,246 / 1,422,995 | 2,095,276 / 22,672 |
| 19,980 | 0.4 | 255.410 / 262.532 | 9.614 / 10.081 | 99,753,229 / 1,519,590 | 2,095,276 / 24,965 |

Allocation bytes are Go heap allocations, not storage bytes. The change includes
both conditional loading and the new selection algorithm, so these timings do
not isolate the contribution of either. Lambda 0.4 selection semantics differ
from baseline and need separate held-out quality evaluation.

A separate 20-query warm trace run measured the new stages:

| Rows | Lambda | Hydrated nodes / payload bytes | Vector rows / payload bytes | Lexical (µs) | Hydration (µs) | Selection (µs) | Vector read (µs) |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1,998 | 1.0 | 39 / 23,339 | 0 / 0 | 556 | 465 | 223 | — |
| 1,998 | 0.4 | 39 / 23,339 | 39 / 546 | 568 | 515 | 649 | 360 |
| 19,980 | 1.0 | 200 / 98,356 | 0 / 0 | 2,935 | 2,101 | 1,003 | — |
| 19,980 | 0.4 | 200 / 98,356 | 200 / 2,800 | 3,314 | 2,112 | 3,908 | 2,540 |

Neither graph edges nor graph node-ID lists were read in any case. Selection
timings include vector reads; do not sum both. Payload bytes count materialized
node fields and vector blobs/IDs, not SQLite page reads, FTS internal scans, or
physical disk I/O. Physical storage-byte measurement remains unperformed.

Reproduce with any consumer-exported Batch JSON:

```sh
BRAID_BENCH_BATCH=/path/to/catalog-batch.json go test ./pkg/braid -run '^$' -bench BenchmarkRetrieval -benchtime=100x -count=1
```

Without `BRAID_BENCH_BATCH`, the benchmark creates synthetic text. The benchmark
file intentionally uses APIs available at `b530618`, allowing the same file to
be copied into an isolated baseline checkout for comparison. Pin the input Batch,
Go version, machine, and baseline commit. Use `query.trace: true` separately for
stage measurements; the latency table above excludes trace overhead. The actual
machine-specific catalog is not committed to this repository.

Contract validation additionally covers hard eligibility, exact reads during
publication, atomic/shared/cyclic dependencies, metadata-heavy export bounds,
the three-candidate diversity regression, missing prerequisites, deadline
recovery, and constrained grid selection. Runtime action success, paraphrase
quality, subprocess latency distributions, and non-cooperative provider recovery
remain adapter-level measurements.
