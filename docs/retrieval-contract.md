# Constrained retrieval and context export

Implemented from the September 7, 2026 computer-use backlog. These are generic
contracts: consumers translate application modes, enabled plugins, focus,
overrides, and required checks into Braid inputs. Braid does not infer them.

## Eligibility before limits

Every retriever uses the same parameterized SQLite eligibility expression before
its `candidate_limit`. Existing `types`, `since`, and `exclude_ids` combine with:

```json
{
  "allowed_ids": ["action:split", "context:prefix", "check:focus"],
  "predicate": {
    "op": "and",
    "children": [
      {"op": "eq", "path": "app", "value": "editor"},
      {"op": "contains", "path": "modes", "value": "n"},
      {"op": "eq", "path": "enabled", "value": true}
    ]
  }
}
```

Omitted/null `allowed_ids` is unconstrained. `[]` permits no nodes; exclusions
always win. The Go API uses nil versus a non-nil empty slice, preserved by JSON
round trips. Large ID lists use a JSON table parameter rather than one SQL
parameter per ID. All predicates operate on `attrs`.

| Operator | Input | Meaning |
|---|---|---|
| `eq` | `path`, scalar `value` | Case-sensitive scalar equality |
| `in` | `path`, scalar `values` array | Equality to any value; empty matches nothing |
| `contains` | `path`, scalar `value` | An array contains an equal scalar element |
| `exists` | `path`, boolean `value` | Field is present (`true`) or missing (`false`) |
| `and`, `or` | Nonempty `children` | Explicit conjunction/disjunction |

Paths are dot-separated object identifiers matching
`[A-Za-z_][A-Za-z0-9_]*` at each level. Arbitrary JSON keys remain storable and
exactly readable; this first predicate version does not address keys containing
dots, spaces, or punctuation. Values may be JSON strings, numbers, booleans, or
null. Strings, numbers, and booleans are never coerced into each other. Integer
and real numbers compare numerically using SQLite's JSON numeric representation.
Missing fields fail equality, membership, and containment, including equality to
null. Present null matches `eq: null` and counts as existing. An omitted `value`
has the same value as null for `eq`/`contains`. Containment requires an array;
objects and scalars fail it. Predicate trees allow depth 32, 256 clauses, and at
most 256 values per `in`. Unknown operators and invalid paths/types fail queries.

Filters constrain graph output, **not traversal intermediates or anchor seeds**.
Traversal remains within the bound dataset and may pass through an excluded
node; graph explanations can name its ID. This preserves existing graph
semantics. Consumers needing stricter traversal isolation must use separately
materialized datasets. Required context obeys the same filters as actions, so
include its IDs and metadata alternatives in eligibility constraints.

## Exact reads and revisions

```json
{"id":"keys","method":"get_many","get":{"dataset_id":"bindings","ids":["n","N","?","<C-w>v"],"expected_revision":12}}
```

The Go API is `Engine.GetMany(ctx, GetRequest)`. It returns `dataset`, `nodes`,
and `missing_ids`. IDs are exact and case-sensitive; found and missing IDs each
retain first-occurrence request order, with duplicates removed. `ids` must be an
explicit array; `[]` performs an empty read. An explicit matching `dataset_id`
is required. Records retain their stored attributes and shortcut punctuation.

Queries, raw retrievers, and exports also accept `query.expected_revision`.
The optional nonnegative precondition is checked in the same read transaction
that supplies every record, dependency, and retrieval stage, before provider
work. A stale revision returns `revision_conflict`; mismatched scope returns
`dataset_mismatch`. A concurrent publisher may commit while a pinned reader
finishes its older consistent snapshot. This is a current-generation precondition,
not historical revision storage. Clients can retry their complete planning read
after recollecting the current dataset identity.

## Required context and selection

```json
{
  "text": "split editor",
  "requires": {
    "action:split": ["context:prefix", "check:focus"],
    "context:prefix": ["context:override"]
  },
  "mandatory_ids": ["context:environment"],
  "budget": {"max": 800, "fixed": 50, "unit": "adapter_units", "estimator": "adapter-v1"}
}
```

`requires` maps any node ID to IDs that must accompany it, transitively. Graph
edges alone impose no dependency. Prerequisites are fetched by exact ID inside
the query snapshot even when absent from every ranked list. A root and its full
closure enter together or are rejected together. Shared nodes are charged once;
cycles form finite bundles. `mandatory_ids` and their closures enter before
optional actions. A candidate already included as a prerequisite is not repeated.
Prerequisites need hard eligibility, but do not need their own relevance score
or evidence-policy support.

Missing or forbidden required nodes reject an action with
`required_context_unavailable`; the response does not reveal rejected prerequisite
content or distinguish whether the ID exists. Optional bundles that cannot fit
produce `budget`. Mandatory content or fixed overhead that cannot fit produces
an empty result with `infeasible_budget`; missing/forbidden mandatory context
produces `required_context_unavailable`. No partial mandatory bundle is returned.

Ranking version **`mmr-admitted-dependencies-v2`** replaces the shortlist followed
by score/cost sorting. At each step Braid chooses the highest MMR score among
complete bundles that fit the remaining budget. Relevance is the root's boosted
fused score divided by the maximum candidate score. Redundancy is the maximum
similarity between newly added bundle members and context already admitted.
Shared prerequisites already admitted are omitted from this comparison. Skipped
bundles never contribute redundancy. Similarity is nonnegative cosine when both
vectors exist, otherwise token-set Jaccard. Ties follow fused score then ID.
There is no subsequent score/cost sort. Lambda 1 chooses relevance alone among
fitting bundles. This is deterministic greedy selection, not a globally optimal
knapsack solver or a diversity quota.

Each retriever still returns at most `candidate_limit` hits, and packing admits
at most that many optional roots. Dependencies and mandatory nodes can increase
the item count. `considered` counts positive fused candidates before evidence
policy; `dropped_for_budget` counts rejected optional roots examined by selection.
`rejections` gives root IDs and reasons. `lexical_status` distinguishes `matched`,
`no_matches`, and `filtered_out` (using an existence probe when a filtered lexical
list is empty). An empty result caused by that last case reports `eligibility`;
other reasons include `no_candidates`, `evidence_policy`, and bundle failures.

## Costs and bounded export

Existing cost priority is unchanged: custom `CostFunc`, explicit node cost,
configured attribute cost, then text rune count divided by four, rounded up with
a minimum of one. Zero-cost nodes and zero budgets remain valid. These units
are arbitrary; the fallback is not a token-exact estimate of metadata or output.

`budget.fixed` reserves consumer-owned overhead. `total_cost` includes it.
`accounting` reports `fixed`, optional root `content`, and `required` costs, plus
the declared `unit` and `estimator`. A shared node retains the category under
which it first entered. The default unit is `cost_units`; estimator identity
describes the configured fallback chain. A failed mandatory reservation reports
zero delivered cost. Consumer callbacks must cooperate with cancellation themselves.

Use `Engine.Export(ctx, ExportRequest)` or `export_context` for a byte-bounded
model payload:

```json
{"id":"context","method":"export_context","timeout_ms":250,"export":{"query":{"dataset_id":"bindings","expected_revision":12,"text":"split editor","requires":{"action:split":["context:prefix"]},"weights":{"lexical":1,"dense":0,"graph":0,"temporal":0},"budget":{"max":800}},"output":{"fields":["id","text","attrs"],"attrs":["shortcut","modes","source"],"max_bytes":4096}}}
```

`output.fields` can select `id`, `type`, `text`, `ts`, `attrs`, `score`, `cost`, and
`why`; IDs are always included. Omitted/null fields default to `id,type,text,attrs`;
an empty array emits IDs alone. `output.attrs` selects exact top-level attribute
keys: omitted/null includes all attributes, `[]` includes none. Explanations are
omitted by default, and when selected their serialized bytes count against the
same limit. Unknown output fields fail validation.

The `context` member is a JSON array, and `context_bytes` is its exact UTF-8 JSON
length, including escaping, metadata, explanations, commas, and the two array
brackets. `max_bytes` must be at least 2. Selection enforces both this limit and
the arbitrary cost budget, always preserving complete bundles. The dataset,
accounting, projection identity, rejection diagnostics, and optional stage trace
are a **separate diagnostic envelope outside the context-byte limit**. The full
subprocess response is not byte-bounded. Consumers should deliver only `context`
to the model and budget any surrounding prompt themselves. No tokenizer or
token-exact rendering contract is supplied.

## Stage work and deadlines

Lexical-only queries hydrate only fused candidate IDs and exact prerequisites.
Graph structures load only for active anchored graph retrieval or anchored
temporal-neighborhood behavior. Dense retrieval still uses brute-force vectors;
when it is disabled, diversity loads vectors only for candidates and required
context that can participate. Lambda 1 does not load diversity vectors. Merely
setting dense weight to zero does not disable vector-based diversity.

`query.trace: true` includes per-stage elapsed microseconds, rows materialized,
and decoded payload bytes. Bytes are logical decoded fields/blobs, **not physical
disk I/O**. Timings overlap (retrieval/selection include their internal reads),
so do not sum every stage as total latency. Tracing is opt-in to preserve stable
ordinary results. See [benchmark method and results](retrieval-benchmarks.md).

Negotiate `query_timeout` and pass `timeout_ms` on `query`, `explain`, `get_many`,
or `export_context`. Values are integer milliseconds from 1 through 86,400,000.
Omitted/null means no additional request timeout. The deadline begins after
request validation, covers waiting for engine access and execution, and propagates
through SQL, providers, and iterative selection. The server returns `cancelled`
and can handle its next request. Dispatch remains sequential; there is no
query-cancel message. Input arrival, parsing, and blocked response writes are not
covered. A caller watchdog remains necessary for providers or arbitrary consumer
code that ignores contexts and for process failures.

## Evaluation gates

Labels may add `forbidden_ids` and an `output` projection/byte limit. Evaluation
reports forbidden-hit and missing-required rates across complete returned lists,
and budget-overrun rates for packed outputs (`budget_cases` identifies their
denominator). Each rate is the fraction of queries with at least one violation.
Missing mandatory context counts as a completeness failure even on abstention.
Expected IDs cannot also be forbidden. Ranking baselines retain their raw-list
semantics; only the Braid condition and grid trials receive budget accounting.

Grid YAML may declare:

```yaml
weights:
  lexical: [0.5, 1.0]
constraints:
  max_forbidden_hit_rate: 0
  max_missing_required_rate: 0
  max_budget_overrun_rate: 0
```

Trials report `eligible` and `violations`. Only eligible trials can win. If all
fail, the report has `status: no_feasible_configuration`, an empty best condition,
and no usable proposal; the CLI writes no proposal file. Limits are optional
rates in [0,1]. An unconstrained grid retains its previous winner policy.
Reports retain dataset/config/ranking identities, effective queries, optional
projection, accounting, and opt-in stage timings. Cancellation is enforced by
contract tests, rather than treated as a ranking metric; an evaluation context
cancellation aborts the run. Actual keyboard correctness and held-out retrieval
quality remain adapter evaluation work.
