# No-answer and negative evaluation

Positive labels retain `expected` IDs. A no-answer case must explicitly use `no_answer: true` and no expected IDs; an accidentally missing label is not treated as a negative. Contradictory expected/no-answer or expected/stale/wrong-workstream IDs are rejected.

```json
{"query_id":"unassigned","query":{"text":"unrelated capture","require_evidence":true,"at":"2026-09-04T12:00:00Z"},"no_answer":true,"expected":[],"stale_ids":["task:closed"],"cross_workstream_ids":["task:other-project"]}
```

`stale_ids` and `cross_workstream_ids` are explicit forbidden-hit annotations for that query, not engine filters. They describe inappropriate candidates within the bound dataset (such as an obsolete task). Querying another dataset remains an error; wrong-scope access is covered by boundary tests rather than converted into a successful negative result.

Every baseline and grid trial reports:

- Recall@5, recall@20, MRR and nDCG@10 averaged over **answerable cases only**, with `answerable_queries` and `no_answer_queries` counts.
- `coverage`: fraction of all queries with any returned items; `abstention_rate`: fraction returning none.
- `no_answer_false_positive_rate`: fraction of no-answer cases returning any item.
- `precision_at_5`: relevant returned IDs divided by all returned IDs in the first five, aggregated across all cases. Unlisted IDs count as irrelevant; labels should be exhaustive for this metric.
- `stale_hit_rate_at_5` and `cross_workstream_hit_rate_at_5`: fraction of cases with the respective annotations whose first five contain an annotated ID. Each includes its eligible case count.

Zero denominators produce zero, accompanied by counts where relevant. A no-answer-only dataset is valid for eval; ranking grid search requires at least one answerable case. Grid selection still optimizes the selected ranking metric, not a combined safety objective: review negative metrics before adopting any proposal. High abstention alone is not success; inspect recall and coverage together.

Full label metadata persists in `evaluation_labels`; the legacy `labels` table is retained. No-answer/annotation fields participate in the grid labels hash. All conditions still read the same dataset snapshot.

## Optional query abstention policy

`require_evidence: true` excludes candidates supported only by recency; an active lexical, dense or graph hit is required. `min_score` optionally excludes boosted fused scores below a nonnegative threshold. Both apply before diversity selection/packing. Defaults preserve existing retrieval behavior. Scores are ranking values, not confidence probabilities; calibrate thresholds on held-out consumer labels. Graph anchors count as graph evidence, and a dense hit by itself is not proof of assignment correctness.

Results include `abstained` and, when empty, an `abstention_reason`: `no_candidates`, `evidence_policy`, or `budget`. Empty output caused by filters or budget is also counted as abstention in evaluation. Explained raw rankings remain available for diagnosis.

Raw retriever and RRF baselines retain their unfiltered ranked-list semantics; query abstention policy is measured on the full `braid` condition and grid trials. This exposes a recency-only baseline's false positives without silently applying a fused-score threshold to incomparable raw scores. Heimdall still owns calibrated assignment decisions, source-revision checks, and acceptance-time validation.
