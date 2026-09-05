$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$localGo = Join-Path $projectRoot '.tools\go\bin\go.exe'
if (Test-Path -LiteralPath $localGo) {
    $env:GOPATH = Join-Path $projectRoot '.tools\gopath'
    $env:GOCACHE = Join-Path $projectRoot '.tools\gocache'
    & $localGo @args
} else {
    & go @args
}
exit $LASTEXITCODE
