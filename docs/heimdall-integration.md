# Heimdall integration

Heimdall is the current consumer priority. Engine APIs and synthetic contract checks are implemented; actual adapter deployment and held-out assignment evaluation are not included here.

## Scope and publication

1. Export a consistent Heimdall snapshot containing only permitted, nonpurged entities. Use stable IDs and semantic change timestamps, not index build time.
2. Configure an explicit dataset ID per account/project scope and a private SQLite file. Do not reuse Heimdall's own database.
3. Launch the pinned Braid executable with `serve --stdio`, an explicit working directory and config path. Negotiate version 1, dataset binding, snapshot/update methods and any background embedding capabilities needed.
4. Check hello's dataset identity. Read the current revision and publish using `replace_snapshot` or `apply` with that expected revision. Apply supports node/edge deletion and preserves unchanged vectors; full replacement removes omitted vectors.
5. If embeddings are configured, start reindex and poll terminal status while keeping the connection open. A revision conflict means the source changed during the build; reconcile before retrying.
6. Record the source boundary, mapping version, dataset ID/revision/digest, config hash, ranking version, binary fingerprint, provider and model. Revalidate proposal targets against current Heimdall state at acceptance.

For snapshots exceeding the request limit, assemble a private unpublished database generation with bounded updates, validate it, then switch the adapter's published handle. There is no staged multi-request transaction API; readers must not see a partially assembled generation.

## Queries and assignment

Queries can use anchors without text. Filter returned candidates to appropriate types. Filters control output eligibility; anchors may still support traversal. Scope is enforced by the bound dataset, not an arbitrary attrs predicate.

`require_evidence: true` rejects recency-only candidates. Minimum fused score thresholds require calibration: scores are not probabilities, graph anchors count as evidence, and dense matches alone do not establish task assignment. Preserve `why` and the full query with each proposal.

Split held-out labels by conversation/artifact lineage. Include ambiguous/unassigned cases, repeated surfaces, obsolete targets and wrong-workstream candidates. Exclude the held-out known assignment from indexed evidence. Compare recall with precision, coverage, abstention and false positives; measure acceptance outcomes in Heimdall separately.

## Process ownership

Use one request at a time on the connection; background reindex runs independently. Match response IDs and protocol versions, reject invalid response shapes, and bound client buffers and deadlines. After a mutation timeout, inspect dataset state before retrying: timeout does not prove failure. Supervise the child and capture stderr separately.

The [PowerShell contract checker](../scripts/verify-contract.ps1) checks negotiation, strict errors, retrieval, legacy replay, dataset identity, snapshot publication, stale-revision rejection and deletion in a fresh temporary database.

References: [protocol](protocol.md), [dataset APIs](datasets.md), [reindex jobs](reindex.md), [grid search](grid-search.md), [negative evaluation](negative-evaluation.md).
