# Creates only the separately authorized, named Docker acceptance stack.
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'Use-DevEnvironment.ps1')
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
try {
    $private=Join-Path $root '.local\docker-acceptance'
    if (-not (Test-Path -LiteralPath $private)) {
        go run ./tests/fixture -init $private
        if ($LASTEXITCODE -ne 0) { throw 'Private fixture creation failed' }
    }
    $core='E:\SDK\Xray\v26.3.27-linux-amd64'
    $digest=[regex]::Match([IO.File]::ReadAllText((Join-Path $core 'Xray-linux-64.zip.dgst')),'SHA2-256=\s*([a-f0-9]{64})').Groups[1].Value
    if ($digest.Length -ne 64 -or (Get-FileHash (Join-Path $core 'Xray-linux-64.zip') -Algorithm SHA256).Hash.ToLowerInvariant() -ne $digest) { throw 'Official Xray digest mismatch' }
    Expand-Archive -LiteralPath (Join-Path $core 'Xray-linux-64.zip') -DestinationPath $core -Force
    $stage=Join-Path $env:PI_SCRATCH_DIR 'docker-context'
    New-Item -ItemType Directory -Force $stage | Out-Null
    foreach ($asset in @('xray','geoip.dat','geosite.dat')) { Copy-Item (Join-Path $core $asset) $stage -Force }
    Copy-Item .\deploy\Dockerfile.acceptance (Join-Path $stage 'Dockerfile') -Force
    if (-not (Test-Path (Join-Path $stage 'migrations'))) { Copy-Item .\migrations $stage -Recurse } else { Copy-Item .\migrations\*.sql (Join-Path $stage 'migrations') -Force }
    $oldOS=$env:GOOS; $oldArch=$env:GOARCH; $oldCGO=$env:CGO_ENABLED
    try {
        $env:GOOS='linux';$env:GOARCH='amd64';$env:CGO_ENABLED='0'
        foreach ($entry in @(@('panel','./cmd/panel'),@('agent','./cmd/agent'),@('fixture','./tests/fixture'))) {
            go build -trimpath -o (Join-Path $stage $entry[0]) $entry[1]
            if ($LASTEXITCODE -ne 0) { throw "Build failed: $($entry[0])" }
        }
    } finally { $env:GOOS=$oldOS;$env:GOARCH=$oldArch;$env:CGO_ENABLED=$oldCGO }
    & 'E:\SDK\Git\bin\bash.exe' -n ./deploy/docker-acceptance.sh
    if ($LASTEXITCODE -ne 0) { throw 'Acceptance shell syntax failed' }
    $archive=Join-Path $env:PI_SCRATCH_DIR 'docker-context.tar'
    tar -cf $archive -C $stage .
    if ($LASTEXITCODE -ne 0) { throw 'Context archive failed' }
    $key=Join-Path $root '.local\database-ssh-key';$known=Join-Path $root '.local\database-known-hosts'
    $args=@('-i',$key,'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o',"UserKnownHostsFile=$known",'-o','ConnectTimeout=10')
    $remote='/opt/proxypanel-acceptance-20261021'
    & ssh @args root@192.168.5.203 'set -eu; d=/opt/proxypanel-acceptance-20261021; if [ -e "$d" ]; then test "$(cat "$d/.proxypanel-acceptance")" = 20261021; else umask 077; mkdir "$d"; printf "20261021\n" > "$d/.proxypanel-acceptance"; fi; mkdir -p "$d/context" "$d/secrets"; chmod 700 "$d" "$d/secrets"'
    if ($LASTEXITCODE -ne 0) { throw 'Remote ownership guard failed' }
    & scp @args $archive "root@192.168.5.203:$remote/context.tar"
    if ($LASTEXITCODE -ne 0) { throw 'Context upload failed' }
    & scp @args .\deploy\docker-acceptance.sh "root@192.168.5.203:$remote/control.sh"
    if ($LASTEXITCODE -ne 0) { throw 'Control upload failed' }
    foreach ($name in @('ca.pem','tls.key','panel.env','postgres.env','redis.conf')) {
        & scp @args (Join-Path $private $name) "root@192.168.5.203:$remote/secrets/$name"
        if ($LASTEXITCODE -ne 0) { throw 'Private fixture upload failed' }
    }
    & ssh @args root@192.168.5.203 'set -eu; cd /opt/proxypanel-acceptance-20261021; chmod 600 secrets/*; chmod 644 secrets/ca.pem secrets/redis.conf; tar -xf context.tar -C context; docker build --label io.proxypanel.acceptance=20261021 -t proxypanel-acceptance:20261021 context; sh control.sh up; sh control.sh inspect'
    if ($LASTEXITCODE -ne 0) { throw 'Docker initialization failed' }
} finally { Pop-Location }
