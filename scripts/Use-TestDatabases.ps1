# Dot-source only in a process that needs the isolated test databases.
$ErrorActionPreference = 'Stop'
$path = Join-Path $PSScriptRoot '..\.local\test-databases.json'
if (-not (Test-Path -LiteralPath $path)) { throw 'Run Start-DatabaseTunnel.ps1 first.' }
$databaseConfig = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
$pg = [Uri]$databaseConfig.database_url
$redis = [Uri]$databaseConfig.redis_url
if ($pg.Host -ne '127.0.0.1' -or $pg.Port -ne 15432 -or $pg.AbsolutePath -ne '/proxypanel') { throw 'Refusing non-project PostgreSQL endpoint' }
if ($redis.Host -ne '127.0.0.1' -or $redis.Port -ne 16379 -or $redis.AbsolutePath -ne '/0') { throw 'Refusing non-project Redis endpoint' }
$env:DATABASE_URL = $databaseConfig.database_url
$env:REDIS_URL = $databaseConfig.redis_url
$databaseConfig = $null
