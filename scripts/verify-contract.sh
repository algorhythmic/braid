#!/usr/bin/env bash
# Uses a fresh temporary database; retains scratch files for inspection.
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
for dependency in jq timeout; do
    if ! command -v "$dependency" >/dev/null 2>&1; then
        printf 'Required command is missing: %s\n' "$dependency" >&2
        exit 127
    fi
done

scratch=$(mktemp -d "${TMPDIR:-/tmp}/braid-contract.XXXXXX")
printf 'Contract scratch files: %s\n' "$scratch" >&2

# JSON is also valid YAML and safely encodes spaces, quotes, and other path characters.
jq -n --arg path "$scratch/index.db" '{
    version: 1,
    dataset_id: "heimdall:contract",
    store: {backend: "sqlite", path: $path},
    embed: {provider: "none"}
}' > "$scratch/braid.yaml"

jq -cnr '
    {id: "heimdall:task:example", type: "task", text: "Example project follow-up",
     ts: "2026-09-04T18:00:00Z", cost: 8, attrs: {task_id: "example", revision: 1}} as $node |
    {text: "Example", filters: {types: ["task"]}, budget: {max: 500},
     explain: true, at: "2026-09-04T20:00:00Z"} as $query |
    [
        {id: "hello", method: "hello", versions: [1], required_capabilities:
            ["strict_requests", "structured_errors", "upsert", "query",
             "dataset_binding", "replace_snapshot", "apply"]},
        {id: "build", method: "upsert", batch: {nodes: [$node], edges: []}},
        "{malformed",
        {id: "invalid", method: "query", query: {filters: {account: "unsupported"}}},
        {id: "first", method: "query", query: $query},
        {id: "second", method: "query", query: $query},
        {id: "replay", method: "replay"},
        {id: "after", method: "query", query: $query},
        {id: "snapshot", method: "replace_snapshot", snapshot: {
            dataset_id: "heimdall:contract", expected_revision: 2,
            batch: {nodes: [$node], edges: []}}},
        {id: "stale", method: "apply", update: {
            dataset_id: "heimdall:contract", expected_revision: 2, delete_nodes: [$node.id]}},
        {id: "delete", method: "apply", update: {
            dataset_id: "heimdall:contract", expected_revision: 3, delete_nodes: [$node.id]}},
        {id: "deleted", method: "query", query: $query}
    ][] | if type == "string" then . else tojson end
' > "$scratch/requests.jsonl"

"$script_dir/dev.sh" build -o "$scratch/braid" ./cmd/braid
timeout --kill-after=5s 30s "$scratch/braid" serve --stdio --config "$scratch/braid.yaml" \
    < "$scratch/requests.jsonl" > "$scratch/responses.jsonl"

if ! jq -se '
    length == 12 and
    [.[].id] == ["hello", "build", null, null, "first", "second", "replay",
                "after", "snapshot", "stale", "delete", "deleted"] and
    all(.[]; .protocol_version == 1 and (has("result") != has("error"))) and
    .[0].result.protocol_version == 1 and
    .[0].result.dataset.id == "heimdall:contract" and
    .[1].result.ok == true and
    .[2].error.code == "parse_error" and
    .[3].error.code == "invalid_request" and
    (.[4].result.items | length) == 1 and
    .[4].result.items[0].id == "heimdall:task:example" and
    (.[4].result.items[0].why | length) > 0 and
    .[4].result.total_cost == 8 and
    .[6].result.ok == true and
    (.[7].result.items | length) == 0 and
    .[8].result.revision == 3 and
    .[9].error.code == "revision_conflict" and
    .[10].result.revision == 4 and
    (.[11].result.items | length) == 0 and
    .[11].result.dataset.revision == 4
' "$scratch/responses.jsonl" >/dev/null; then
    printf 'Contract verification failed; inspect %s/responses.jsonl\n' "$scratch" >&2
    exit 1
fi

# Compare the actual wire bytes, excluding only the echoed request ID.
mapfile -t wire < "$scratch/responses.jsonl"
result_prefix='"result":'
if [[ ${#wire[@]} -ne 12 || "${wire[4]#*"$result_prefix"}" != "${wire[5]#*"$result_prefix"}" ]]; then
    printf 'Expected 12 response lines and byte-identical repeated results; inspect %s\n' "$scratch" >&2
    exit 1
fi

jq -n --arg scratch "$scratch" '{
    status: "passed",
    checks: ["version/capability negotiation", "upsert", "structured parse errors",
             "unknown nested filter rejection", "query recovery and explanation",
             "explicit cost", "byte-identical repeated result", "legacy replay caveat",
             "dataset binding", "snapshot publication", "stale revision rejection",
             "transactional deletion"],
    scratch: $scratch
}'
