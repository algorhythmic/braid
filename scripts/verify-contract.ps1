# Read-only against Braid source and existing databases. Scratch data is retained in TEMP.
$ErrorActionPreference = 'Stop'
$braidRoot = Split-Path -Parent $PSScriptRoot
$taskGo = Join-Path $braidRoot '.tools/go/bin/go.exe'
if (Test-Path -LiteralPath $taskGo) {
    $env:GOPATH = Join-Path $braidRoot '.tools/gopath'
    $env:GOCACHE = Join-Path $braidRoot '.tools/gocache'
} else {
    $taskGo = (Get-Command go -ErrorAction Stop).Source
}
$scratch = Join-Path ([System.IO.Path]::GetTempPath()) ('heimdall-braid-contract-' + [guid]::NewGuid().ToString('N'))
[void][System.IO.Directory]::CreateDirectory($scratch)
$configPath = Join-Path $scratch 'braid.yaml'
$dbPath = (Join-Path $scratch 'index.db').Replace('\', '/')
$config = "version: 1`ndataset_id: heimdall:contract`nstore: {backend: sqlite, path: '$($dbPath.Replace("'", "''"))'}`nembed: {provider: none}`n"
[System.IO.File]::WriteAllText($configPath, $config, [System.Text.UTF8Encoding]::new($false))
$node = @{ id = 'heimdall:task:example'; type = 'task'; text = 'Example project follow-up'; ts = '2026-09-04T18:00:00Z'; cost = 8; attrs = @{task_id = 'example'; revision = 1} }
$query = @{text = 'Example'; filters = @{types = @('task')}; budget = @{max = 500}; explain = $true; at = '2026-09-04T20:00:00Z'}
$requests = @(
    (@{id = 'hello'; method = 'hello'; versions = @(1); required_capabilities = @('strict_requests', 'structured_errors', 'upsert', 'query', 'dataset_binding', 'replace_snapshot', 'apply')} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'build'; method = 'upsert'; batch = @{nodes = @($node); edges = @()}} | ConvertTo-Json -Depth 12 -Compress)
    '{malformed'
    '{"id":"invalid","method":"query","query":{"filters":{"account":"unsupported"}}}'
    (@{id = 'first'; method = 'query'; query = $query} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'second'; method = 'query'; query = $query} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'replay'; method = 'replay'} | ConvertTo-Json -Compress)
    (@{id = 'after'; method = 'query'; query = $query} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'snapshot'; method = 'replace_snapshot'; snapshot = @{dataset_id = 'heimdall:contract'; expected_revision = 2; batch = @{nodes = @($node); edges = @()}}} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'stale'; method = 'apply'; update = @{dataset_id = 'heimdall:contract'; expected_revision = 2; delete_nodes = @($node.id)}} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'delete'; method = 'apply'; update = @{dataset_id = 'heimdall:contract'; expected_revision = 3; delete_nodes = @($node.id)}} | ConvertTo-Json -Depth 12 -Compress)
    (@{id = 'deleted'; method = 'query'; query = $query} | ConvertTo-Json -Depth 12 -Compress)
)
Push-Location $braidRoot
try {
    $raw = @($requests | & $taskGo run ./cmd/braid serve --stdio --config $configPath)
    if ($LASTEXITCODE -ne 0) { throw "Braid subprocess failed: $LASTEXITCODE" }
} finally { Pop-Location }
if ($raw.Count -ne 12) { throw "Expected 12 response lines, got $($raw.Count)" }
$responses = @($raw | ForEach-Object { $_ | ConvertFrom-Json })
if ($responses[0].id -ne 'hello' -or $responses[0].result.protocol_version -ne 1) { throw 'Negotiation failed' }
if ($responses[1].id -ne 'build' -or -not $responses[1].result.ok) { throw 'Upsert failed' }
if ($responses[2].error.code -ne 'parse_error') { throw 'Malformed request did not return a structured parse error' }
if ($responses[3].error.code -ne 'invalid_request') { throw 'Unknown nested filter was silently accepted' }
if ($responses[4].id -ne 'first' -or $responses[4].result.items.Count -ne 1) { throw 'Query did not recover after malformed request' }
if ($responses[4].result.items[0].id -ne $node.id) { throw 'Wrong query item' }
if ($responses[4].result.items[0].why.Count -eq 0) { throw 'Missing explanation' }
if ($responses[4].result.total_cost -ne 8) { throw 'Wrong explicit node cost' }
# Compare serialized result bytes from the actual wire, excluding only the echoed request ID.
$wireFirst = $raw[4].Substring($raw[4].IndexOf('"result":'))
$wireSecond = $raw[5].Substring($raw[5].IndexOf('"result":'))
if ($wireFirst -cne $wireSecond) { throw 'Unchanged query result is not byte-identical' }
if ($responses[6].id -ne 'replay' -or -not $responses[6].result.ok) { throw 'Replay failed' }
if ($responses[7].id -ne 'after' -or $responses[7].result.items.Count -ne 0) { throw 'Expected replay to remove direct upserts' }
if ($responses[0].result.dataset.id -ne 'heimdall:contract') { throw 'Wrong bound dataset' }
if ($responses[8].result.revision -ne 3) { throw 'Snapshot publication failed' }
if ($responses[9].error.code -ne 'revision_conflict') { throw 'Stale revision was accepted' }
if ($responses[10].result.revision -ne 4) { throw 'Deletion publication failed' }
if ($responses[11].result.items.Count -ne 0 -or $responses[11].result.dataset.revision -ne 4) { throw 'Deleted content remained searchable or revision mismatch' }
[pscustomobject]@{
    status = 'passed'
    checks = @('version/capability negotiation', 'upsert', 'structured parse errors', 'unknown nested filter rejection', 'query recovery and explanation', 'explicit cost', 'byte-identical repeated result', 'legacy replay caveat', 'dataset binding', 'snapshot publication', 'stale revision rejection', 'transactional deletion')
    scratch = $scratch
} | ConvertTo-Json -Depth 4
