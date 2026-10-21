# Prepares the version-switch artifacts for the Docker lifecycle acceptance
# (goal criterion 8 / A13). Local only: it never opens SSH, never talks to
# Docker and never touches the running acceptance stack. Everything it creates
# stays inside the workspace (source) or the session scratch directory.
#
# It produces:
#   - a provenance record of the older build/<platform>/agent artifacts (SHA256
#     and the version the windows one really reports). These are NOT the switch
#     baseline: the acceptance image ships its own agent 0.1.0 which already
#     contains earlier fixes, and the real baseline is measured on the host by
#     `sh <remote>/lifecycle.sh baseline` (version + sha256 of /app/agent);
#   - a fresh agent build from the current source (0.1.1) for linux/amd64,
#     verified by running the same source built for windows/amd64 and asking it
#     `-version`;
#   - the official Xray core release selected as the second whitelist entry
#     (v26.3.23) for linux/amd64 (container) and windows/amd64 (local real-core
#     tests), each verified against the release's official SHA2-256 digest.
#
# The parent uploads only the files listed in the printed manifest. The staged
# core resolves geoip.dat/geosite.dat next to its own executable, so the two
# official geo assets of the same release belong to the set:
#   scp <out>/agent-new <out>/xray-core <out>/geoip.dat <out>/geosite.dat `
#       <out>/SHA256SUMS root@host:$remote/lifecycle/
param(
    [string]$OutputDirectory = (Join-Path $env:PI_SCRATCH_DIR 'lifecycle-upload'),
    [string]$CoreVersion = '26.3.23',
    [switch]$SkipDownload,
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Use-DevEnvironment.ps1')
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
try {
    New-Item -ItemType Directory -Force $OutputDirectory | Out-Null
    $baseline = [ordered]@{}

    # ---------- 1. baseline of the already shipped build ----------
    # The existing build/ directories are older artifacts from before this
    # round's fixes. They are the "old version" side of the upgrade and are only
    # read here: nothing is rebuilt into them and nothing is overwritten.
    foreach ($platform in @('linux-amd64', 'windows-amd64')) {
        $directory = Join-Path $root "build\$platform"
        $binary = if ($platform -eq 'windows-amd64') { Join-Path $directory 'agent.exe' } else { Join-Path $directory 'agent' }
        if (-not (Test-Path -LiteralPath $binary)) { throw "Baseline binary is missing: $binary" }
        $sums = Join-Path $directory 'SHA256SUMS'
        if (-not (Test-Path -LiteralPath $sums)) { throw "Baseline manifest is missing: $sums" }
        $name = Split-Path $binary -Leaf
        $recorded = (Select-String -LiteralPath $sums -Pattern "^([0-9a-f]{64})  $([regex]::Escape($name))$" |
            Select-Object -First 1).Matches.Groups[1].Value
        $actual = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
        if (-not $recorded) { throw "No SHA256 recorded for $platform/$name" }
        if ($recorded -ne $actual) { throw "Baseline $platform/$name does not match its own SHA256SUMS (recorded $recorded, actual $actual)" }
        $version = 'not executable on this host'
        if ($platform -eq 'windows-amd64') {
            $version = (& $binary -version | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) { throw "Baseline agent -version failed: $version" }
        }
        $baseline[$platform] = [pscustomobject]@{
            Path = "build/$platform/$name"; SHA256 = $actual; Bytes = (Get-Item -LiteralPath $binary).Length
            Version = $version; Marker = (Get-Content -LiteralPath (Join-Path $directory '.proxypanel-build') -Raw).Trim()
        }
    }
    # The version this round ships must really differ from the baseline one.
    $sourceVersion = [regex]::Match([IO.File]::ReadAllText((Join-Path $root 'internal\agent\config\config.go')),
        'const AgentVersion = "([0-9]+\.[0-9]+\.[0-9]+)"').Groups[1].Value
    if ($sourceVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw 'Cannot read AgentVersion from internal/agent/config/config.go' }
    if ($baseline['windows-amd64'].Version -notmatch [regex]::Escape($sourceVersion)) {
        Write-Host "baseline agent version: $($baseline['windows-amd64'].Version)"
        Write-Host "current source version:  $sourceVersion"
    } else {
        throw "The current source still reports the baseline version ${sourceVersion}: a rebuild alone would not be an upgrade"
    }

    # ---------- 2. current agent build for linux/amd64 ----------
    if (-not $SkipBuild) {
        $oldOS = $env:GOOS; $oldArch = $env:GOARCH; $oldCGO = $env:CGO_ENABLED
        try {
            $env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
            # -buildvcs=false keeps the artifact reproducible and keeps local VCS
            # metadata out of the uploaded binary.
            go build -trimpath -buildvcs=false -o (Join-Path $OutputDirectory 'agent-new') ./cmd/agent
            if ($LASTEXITCODE -ne 0) { throw 'Agent build for linux/amd64 failed' }
            # Same source, host executable: proves the version string the real
            # container binary will report to the panel.
            $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
            $proofPath = Join-Path $env:PI_SCRATCH_DIR 'agent-version-proof.exe'
            go build -trimpath -buildvcs=false -o $proofPath ./cmd/agent
            if ($LASTEXITCODE -ne 0) { throw 'Agent build for the local version proof failed' }
        } finally { $env:GOOS = $oldOS; $env:GOARCH = $oldArch; $env:CGO_ENABLED = $oldCGO }
        $proof = (& $proofPath -version | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw "New agent -version failed: $proof" }
        if ($proof -notmatch [regex]::Escape($sourceVersion)) { throw "New agent reports '$proof', expected version $sourceVersion" }
        Write-Host "new agent version proof: $proof"
    } else {
        $proof = 'skipped (-SkipBuild)'
    }

    # ---------- 3. second official Xray core ----------
    $assets = [ordered]@{
        'windows' = @{ Zip = 'Xray-windows-64.zip'; Directory = "E:\SDK\Xray\v$CoreVersion" }
        'linux'   = @{ Zip = 'Xray-linux-64.zip'; Directory = "E:\SDK\Xray\v$CoreVersion-linux-amd64" }
    }
    $release = "https://github.com/XTLS/Xray-core/releases/download/v$CoreVersion"
    $cores = [ordered]@{}
    foreach ($kind in $assets.Keys) {
        $asset = $assets[$kind]
        $directory = $asset.Directory
        New-Item -ItemType Directory -Force $directory | Out-Null
        $zip = Join-Path $directory $asset.Zip
        $digestFile = Join-Path $directory "$($asset.Zip).dgst"
        $binaryName = $kind -eq 'windows' ? 'xray.exe' : 'xray'
        $binary = Join-Path $directory $binaryName
        if (-not $SkipDownload) {
            foreach ($pair in @(@($asset.Zip, $zip), @("$($asset.Zip).dgst", $digestFile))) {
                if (Test-Path -LiteralPath $pair[1]) { Remove-Item -LiteralPath $pair[1] -Force }
                Invoke-WebRequest -Uri "$release/$($pair[0])" -OutFile $pair[1] -TimeoutSec 600 -UseBasicParsing
                if (-not (Test-Path -LiteralPath $pair[1])) { throw "Download failed: $($pair[0])" }
            }
        }
        if (-not (Test-Path -LiteralPath $zip) -or -not (Test-Path -LiteralPath $digestFile)) {
            throw "Missing official Xray $CoreVersion assets in $directory"
        }
        $expected = [regex]::Match([IO.File]::ReadAllText($digestFile), 'SHA2-256=\s*([a-f0-9]{64})').Groups[1].Value
        $actualZip = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($expected.Length -ne 64) { throw "Official digest for $($asset.Zip) is unreadable" }
        if ($expected -ne $actualZip) { throw "Official Xray $CoreVersion $($asset.Zip) digest mismatch (expected $expected, actual $actualZip)" }
        Expand-Archive -LiteralPath $zip -DestinationPath $directory -Force
        if (-not (Test-Path -LiteralPath $binary)) { throw "Extracted core is missing: $binary" }
        # The staged core resolves its assets next to its own executable, so
        # geoip.dat/geosite.dat are part of the switch set. They come from this
        # same already digest-verified official zip and their sizes must match
        # its entries: a leftover file from another release cannot slip in.
        if ($kind -eq 'linux') {
            $geo = @('geoip.dat', 'geosite.dat')
            $archive = [IO.Compression.ZipFile]::OpenRead($zip)
            try {
                foreach ($assetName in $geo) {
                    $entry = $archive.Entries | Where-Object { $_.FullName -eq $assetName } | Select-Object -First 1
                    if (-not $entry) { throw "The official $($asset.Zip) does not contain $assetName" }
                    $geoFile = Join-Path $directory $assetName
                    if (-not (Test-Path -LiteralPath $geoFile)) { throw "The extracted geo asset is missing: $geoFile" }
                    $geoBytes = (Get-Item -LiteralPath $geoFile).Length
                    if ($geoBytes -ne $entry.Length) {
                        throw "$geoFile does not match the official $($asset.Zip) entry ($geoBytes != $($entry.Length) bytes)"
                    }
                }
            } finally { $archive.Dispose() }
            Write-Host "official geo assets ($CoreVersion): $(($geo | ForEach-Object { "$_=$((Get-Item -LiteralPath (Join-Path $directory $_)).Length)" }) -join ' ')"
        }
        $version = 'queried in the container'
        if ($kind -eq 'windows') {
            $version = (& $binary version 2>&1 | Select-Object -First 1 | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) { throw "Xray $CoreVersion version query failed: $version" }
            if ($version -notmatch [regex]::Escape($CoreVersion)) { throw "Downloaded core reports '$version', expected $CoreVersion" }
        }
        $cores[$kind] = [pscustomobject]@{
            Zip = "Xray $CoreVersion $($asset.Zip)"; OfficialSHA256 = $actualZip
            Binary = $binary; BinarySHA256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
            Bytes = (Get-Item -LiteralPath $binary).Length; Version = $version
        }
    }
    foreach ($assetName in @('xray', 'geoip.dat', 'geosite.dat')) {
        $stagedName = $assetName -eq 'xray' ? 'xray-core' : $assetName
        Copy-Item (Join-Path $assets['linux'].Directory $assetName) (Join-Path $OutputDirectory $stagedName) -Force
    }

    # ---------- 4. upload manifest ----------
    # The staged set is one unit: the staged core resolves geoip.dat/geosite.dat
    # next to its own executable, and the remote `stage`/`core-switch` actions
    # refuse a set whose SHA256SUMS does not cover all four files.
    $upload = @('agent-new', 'xray-core', 'geoip.dat', 'geosite.dat')
    $sha = [ordered]@{}
    foreach ($name in $upload) {
        $path = Join-Path $OutputDirectory $name
        if (-not (Test-Path -LiteralPath $path)) { throw "Staged artifact is missing: $path" }
        $sha[$name] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $lines = foreach ($name in $upload) { "$($sha[$name])  $name" }
    # LF line endings: `sha256sum -c` inside the container would otherwise look
    # for "agent-new\r".
    [IO.File]::WriteAllText((Join-Path $OutputDirectory 'SHA256SUMS'), (($lines -join "`n") + "`n"), [Text.Encoding]::ASCII)
    $manifest = @(
        "# Docker lifecycle version-switch artifacts (goal criterion 8 / A13)",
        "generated=$((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))",
        "",
        "[what is NOT the switch baseline]",
        "The build/<platform>/agent artifacts below are older repository artifacts from before this",
        "round. They are recorded for provenance only and are NEVER the baseline of the version",
        "switch: the acceptance image ships its own agent binary, which already contains earlier",
        "fixes. The real switch starts from the image binary, measured on the host by",
        "  sh <remote root>/lifecycle.sh baseline   (version AND sha256 of /app/agent and /app/xray).",
        "linux-amd64/agent    sha256=$($baseline['linux-amd64'].SHA256) bytes=$($baseline['linux-amd64'].Bytes) version=$($baseline['linux-amd64'].Version) marker=$($baseline['linux-amd64'].Marker)",
        "windows-amd64/agent  sha256=$($baseline['windows-amd64'].SHA256) bytes=$($baseline['windows-amd64'].Bytes) version=$($baseline['windows-amd64'].Version)",
        "",
        "[upload set - scp these five files to <remote root>/lifecycle/]",
        "agent-new   sha256=$($sha['agent-new']) version=$proof source_version=$sourceVersion (switch target: new agent binary)",
        "xray-core   sha256=$($sha['xray-core']) version=$CoreVersion (Xray-linux-64.zip sha256=$($cores['linux'].OfficialSHA256) matches the official .dgst; switch target: second verified core)",
        "geoip.dat   sha256=$($sha['geoip.dat']) bytes=$((Get-Item -LiteralPath (Join-Path $OutputDirectory 'geoip.dat')).Length) (official $CoreVersion Xray-linux-64.zip entry; the staged core resolves it next to /s/xray-core)",
        "geosite.dat sha256=$($sha['geosite.dat']) bytes=$((Get-Item -LiteralPath (Join-Path $OutputDirectory 'geosite.dat')).Length) (same official zip entry)",
        "SHA256SUMS four LF lines: agent-new, xray-core, geoip.dat, geosite.dat (~30MB hashed inside the image)",
        "",
        "[local real-core verification binary]",
        "windows xray $CoreVersion = $($cores['windows'].Binary) sha256=$($cores['windows'].BinarySHA256) version=$($cores['windows'].Version)",
        "",
        "No SSH, no Docker and no container was touched by this script."
    )
    Set-Content -LiteralPath (Join-Path $OutputDirectory 'manifest.txt') -Value $manifest -Encoding utf8
    $manifest | ForEach-Object { Write-Host $_ }
    Write-Host "`nStaged artifacts: $OutputDirectory"
} finally { Pop-Location }
