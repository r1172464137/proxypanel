param([string]$Run = '.')
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Use-DevEnvironment.ps1')
$previousPG = $env:DATABASE_URL
$previousRedis = $env:REDIS_URL
$previousTestPG = $env:PANEL_TEST_DATABASE_URL
$previousTestRedis = $env:PANEL_TEST_REDIS_URL
& (Join-Path $PSScriptRoot 'Start-DatabaseTunnel.ps1') -KnownHostsPath (Join-Path $PSScriptRoot '..\.local\database-known-hosts')
$tunnelPid = [int](Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\.local\database-tunnel.pid'))
try {
    . (Join-Path $PSScriptRoot 'Use-TestDatabases.ps1')
    $env:PANEL_TEST_DATABASE_URL = $env:DATABASE_URL
    $env:PANEL_TEST_REDIS_URL = $env:REDIS_URL
    Push-Location (Join-Path $PSScriptRoot '..')
    try {
        go test -v ./internal/panel/api ./internal/panel/store ./internal/panel/maintenance ./tests/e2e -run $Run -count=1 -timeout 240s
        if ($LASTEXITCODE -ne 0) { throw 'Isolated integration tests failed' }
    } finally { Pop-Location }
} finally {
    Get-Process -Id $tunnelPid -ErrorAction SilentlyContinue | Where-Object ProcessName -eq 'ssh' | Stop-Process
    $env:DATABASE_URL = $previousPG
    $env:REDIS_URL = $previousRedis
    $env:PANEL_TEST_DATABASE_URL = $previousTestPG
    $env:PANEL_TEST_REDIS_URL = $previousTestRedis
}
