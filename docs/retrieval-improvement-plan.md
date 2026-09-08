# Retrieval contract implementation plan

Based on the September 7, 2026 wayland-computer-use backlog and source review.
Braid enforces generic constraints; consumers own application applicability,
prerequisites, shortcut identity, and prompt rendering.

1. Compile typed metadata predicates and allowed IDs into shared SQL eligibility,
   applied before each retriever's limit. Keep graph traversal dataset-wide.
2. Add ordered exact batch reads and optional query revision preconditions inside
   the read transaction, before any embedding work.
3. Pack dependency closures atomically, with shared costs, mandatory IDs, explicit
   rejection reasons, and cycle handling. Make diversity depend on admitted items.
4. Add a projected JSON context export with a strict byte bound and cost accounting;
   preserve existing arbitrary node-cost budgets.
5. Plan stages before loading data, hydrate lexical candidates only, restrict MMR
   vector reads to candidates, and expose optional stage measurements.
6. Add negotiated subprocess request timeouts, including cancellable engine access.
7. Add forbidden-result, dependency, budget, and cancellation fixtures; make grid
   proposals conditional on declared invariant limits. Run tests, race checks,
   and reproducible synthetic benchmarks (including 1,998 records).

Ranking changes receive a new version identifier. Measurements distinguish
synthetic fixtures from the actual adapter catalog; no performance claim is made
without a measured baseline.

Implementation status: all seven steps are implemented. The public contract and
deliberate limits are documented in [retrieval-contract.md](retrieval-contract.md).
The catalog benchmark uses 1,998 current binding records and a tenfold expanded
fixture; see [measurements](retrieval-benchmarks.md). Contract tests cover the
backlog's generic invariants. Held-out adapter quality, actual tokenizer/prompt
costs, process watchdog behavior, and UI applicability remain consumer work.
