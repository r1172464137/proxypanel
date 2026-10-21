param([string]$Run='TestDockerAcceptance')
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'Use-DevEnvironment.ps1')
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$key=Join-Path $root '.local\database-ssh-key';$known=Join-Path $root '.local\database-known-hosts'
$ports=@(18443,18444,25432,25433,20101,20102,20103,20104,20105,20201,20202,20203,20204,20205)
foreach ($port in $ports) { $listener=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,$port);try {$listener.Start()} finally {$listener.Stop()} }
$arguments=@('-N','-i',('"'+$key+'"'),'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o',('"UserKnownHostsFile='+$known+'"'),'-o','ExitOnForwardFailure=yes','-o','ServerAliveInterval=15','-o','ServerAliveCountMax=3')
foreach ($port in $ports) { $arguments+=@('-L',"127.0.0.1:${port}:127.0.0.1:$port") }
$arguments+='root@192.168.5.203'
$p=Start-Process -FilePath (Get-Command ssh).Source -ArgumentList $arguments -PassThru -WindowStyle Hidden -RedirectStandardError (Join-Path $env:PI_SCRATCH_DIR 'docker-tunnel.stderr')
$previous=$env:PANEL_DOCKER_TEST;$previousRoot=$env:PANEL_DOCKER_ROOT
try {
    Start-Sleep -Seconds 2
    if ($p.HasExited) { throw 'Docker SSH tunnel failed' }
    $env:PANEL_DOCKER_TEST='1';$env:PANEL_DOCKER_ROOT=$root
    Push-Location $root
    try { go test -v ./tests/e2e -run $Run -count=1 -timeout 8m; if ($LASTEXITCODE -ne 0) { throw 'Docker acceptance tests failed' } } finally {Pop-Location}
} finally {
    if (-not $p.HasExited) { Stop-Process -Id $p.Id }
    $env:PANEL_DOCKER_TEST=$previous;$env:PANEL_DOCKER_ROOT=$previousRoot
}
