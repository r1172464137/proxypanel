param(
    [string]$KeyPath = (Join-Path $PSScriptRoot '..\私钥.txt'),
    [string]$KnownHostsPath = (Join-Path $env:PI_SCRATCH_DIR 'linux-db-known-hosts')
)
$ErrorActionPreference = 'Stop'
$local = Join-Path $PSScriptRoot '..\.local'
New-Item -ItemType Directory -Path $local -Force | Out-Null
$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$acl = Get-Acl -LiteralPath $local
$safe = $acl.AreAccessRulesProtected -and @($acl.Access).Count -gt 0 -and @($acl.Access | Where-Object { $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value -ne $sid.Value }).Count -eq 0
if (-not $safe) {
    $dacl = [System.Security.AccessControl.DirectorySecurity]::new()
    $dacl.SetSecurityDescriptorSddlForm("D:P(A;OICI;FA;;;$($sid.Value))", [System.Security.AccessControl.AccessControlSections]::Access)
    [System.IO.FileSystemAclExtensions]::SetAccessControl([System.IO.DirectoryInfo]::new($local), $dacl)
}
if (-not (Test-Path -LiteralPath $KnownHostsPath)) { throw 'Supply an existing trusted known_hosts file; host verification is required.' }
$key = Join-Path $local 'database-ssh-key'
Copy-Item -LiteralPath $KeyPath -Destination $key -Force
$keyAcl = Get-Acl -LiteralPath $key
$safe = $keyAcl.AreAccessRulesProtected -and @($keyAcl.Access).Count -gt 0 -and @($keyAcl.Access | Where-Object { $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value -ne $sid.Value }).Count -eq 0
if (-not $safe) {
    $dacl = [System.Security.AccessControl.FileSecurity]::new()
    $dacl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$($sid.Value))", [System.Security.AccessControl.AccessControlSections]::Access)
    [System.IO.FileSystemAclExtensions]::SetAccessControl([System.IO.FileInfo]::new($key), $dacl)
}
$known = Join-Path $local 'database-known-hosts'
if ([IO.Path]::GetFullPath($KnownHostsPath) -ne [IO.Path]::GetFullPath($known)) { Copy-Item -LiteralPath $KnownHostsPath -Destination $known -Force }
foreach ($port in @(15432, 16379)) {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $port)
    try { $listener.Start() } finally { $listener.Stop() }
}
$baseArgs = @('-i', $key, '-p', '22', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', "UserKnownHostsFile=$known", '-o', 'ConnectTimeout=10')
# Only application credentials are retrieved; never retrieve the PostgreSQL admin secret.
$remote = 'set -eu; printf ''{"pg_password":"%s","redis_password":"%s"}'' "$(cat /opt/proxypanel-test/secrets/pg-app-password)" "$(cat /opt/proxypanel-test/secrets/redis-password)"'
$raw = & ssh @baseArgs root@192.168.5.203 $remote
if ($LASTEXITCODE -ne 0) { throw 'Cannot retrieve test application credentials' }
$secrets = ($raw -join "`n") | ConvertFrom-Json
if ($secrets.pg_password -notmatch '^[a-f0-9]{64}$' -or $secrets.redis_password -notmatch '^[a-f0-9]{64}$') { throw 'Unexpected credential format' }
$config = @{
    database_url = "postgres://proxypanel:$($secrets.pg_password)@127.0.0.1:15432/proxypanel?sslmode=disable"
    redis_url = "redis://default:$($secrets.redis_password)@127.0.0.1:16379/0"
}
[IO.File]::WriteAllText((Join-Path $local 'test-databases.json'), ($config | ConvertTo-Json))
$raw = $null; $secrets = $null; $config = $null
$arguments = @('-N', '-i', ('"' + $key + '"'), '-p', '22', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', ('"UserKnownHostsFile=' + $known + '"'), '-o', 'ExitOnForwardFailure=yes', '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=3', '-L', '127.0.0.1:15432:127.0.0.1:15432', '-L', '127.0.0.1:16379:127.0.0.1:16379', 'root@192.168.5.203')
$process = Start-Process -FilePath (Get-Command ssh).Source -ArgumentList $arguments -PassThru -WindowStyle Hidden -RedirectStandardError (Join-Path $local 'database-tunnel.stderr')
Start-Sleep -Seconds 2
if ($process.HasExited) { throw 'SSH tunnel failed. Inspect .local/database-tunnel.stderr (no passwords are logged).' }
[IO.File]::WriteAllText((Join-Path $local 'database-tunnel.pid'), [string]$process.Id)
Write-Host "Test database SSH tunnel started (PID $($process.Id)); local ports 15432 and 16379."
Write-Host 'Application credentials saved under restricted .local/, excluded from Git. Original private key unchanged.'
