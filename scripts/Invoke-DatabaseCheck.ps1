$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Use-DevEnvironment.ps1')
$known = Join-Path $PSScriptRoot '..\.local\database-known-hosts'
$previousPG = $env:DATABASE_URL
$previousRedis = $env:REDIS_URL
& (Join-Path $PSScriptRoot 'Start-DatabaseTunnel.ps1') -KnownHostsPath $known
$tunnelPid = [int](Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\.local\database-tunnel.pid'))
try {
    . (Join-Path $PSScriptRoot 'Use-TestDatabases.ps1')
    Push-Location (Join-Path $PSScriptRoot '..')
    try {
        go run ./cmd/dbcheck
        if ($LASTEXITCODE -ne 0) { throw 'Isolated database check failed' }
    } finally { Pop-Location }
} finally {
    Get-Process -Id $tunnelPid -ErrorAction SilentlyContinue | Where-Object ProcessName -eq 'ssh' | Stop-Process
    $env:DATABASE_URL = $previousPG
    $env:REDIS_URL = $previousRedis
}
