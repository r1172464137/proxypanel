param([ValidateSet('windows-amd64','linux-amd64','linux-arm64')][string[]]$Targets = @('windows-amd64','linux-amd64','linux-arm64'))
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
foreach ($target in $Targets) {
    $out = Join-Path $root "build\$target"
    if (-not (Test-Path -LiteralPath (Join-Path $out '.proxypanel-build'))) { throw "Unmarked release: $target" }
    $expected = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($name in @('.proxypanel-build','SHA256SUMS')) { [void]$expected.Add($name) }
    $suffix = if ($target.StartsWith('windows-')) { '.exe' } else { '' }
    foreach ($component in @('panel','agent')) { [void]$expected.Add($component+$suffix) }
    foreach ($directory in @('migrations','configs','deploy','web/dist')) {
        $source = Join-Path $root $directory
        foreach ($file in Get-ChildItem -LiteralPath $source -Recurse -File -Force) {
            $relative = $directory + '/' + [IO.Path]::GetRelativePath($source,$file.FullName).Replace('\','/')
            [void]$expected.Add($relative)
            $copy = Join-Path $out $relative
            if (-not (Test-Path -LiteralPath $copy -PathType Leaf)) { throw "Missing release file: $target/$relative" }
            if ((Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $copy -Algorithm SHA256).Hash) {
                throw "Release differs from source: $target/$relative"
            }
        }
    }
    $files = @(Get-ChildItem -LiteralPath $out -Recurse -File -Force)
    if ($files.Count -ne $expected.Count) { throw "Unexpected file count in $target" }
    foreach ($file in $files) {
        $relative = [IO.Path]::GetRelativePath($out,$file.FullName).Replace('\','/')
        if (-not $expected.Contains($relative)) { throw "Unexpected packaged file: $target/$relative" }
        if ($relative -match '(^|/)(\.local|node_modules|\.git)(/|$)|\.(pem|key|sqlite|log)$|私钥|test-databases') {
            throw "Forbidden packaged path: $target/$relative"
        }
    }
    $manifestBytes = [IO.File]::ReadAllBytes((Join-Path $out 'SHA256SUMS'))
    if ($manifestBytes.Length -eq 0 -or $manifestBytes -contains 13 -or $manifestBytes[0] -eq 0xef -or $manifestBytes[-1] -ne 10) {
        throw "SHA256SUMS must use UTF-8 without BOM and LF line endings: $target"
    }
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($line in [IO.File]::ReadAllLines((Join-Path $out 'SHA256SUMS'))) {
        if ($line -notmatch '^([a-f0-9]{64})  ([^\\]+)$') { throw "Invalid checksum line in $target" }
        $hash = $Matches[1]; $relative = $Matches[2]
        if ($relative -eq 'SHA256SUMS' -or $relative -match '(^/|:|(^|/)\.\.(/|$))' -or -not $expected.Contains($relative)) { throw "Invalid checksum path: $relative" }
        if (-not $seen.Add($relative)) { throw "Duplicate checksum path: $relative" }
        if ((Get-FileHash -LiteralPath (Join-Path $out $relative) -Algorithm SHA256).Hash.ToLowerInvariant() -ne $hash) {
            throw "Checksum mismatch: $target/$relative"
        }
    }
    if ($seen.Count -ne ($expected.Count-1)) { throw "Incomplete checksum manifest: $target" }
    foreach ($component in @('panel','agent')) {
        $bytes = [IO.File]::ReadAllBytes((Join-Path $out ($component+$suffix)))
        if ($suffix -eq '.exe') {
            if ($bytes.Length -lt 64 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) { throw "Not a PE binary: $target/$component" }
            $pe = [BitConverter]::ToInt32($bytes,0x3c)
            if ($pe -lt 0 -or $pe+6 -gt $bytes.Length -or [BitConverter]::ToUInt32($bytes,$pe) -ne 0x4550 -or [BitConverter]::ToUInt16($bytes,$pe+4) -ne 0x8664) { throw "Wrong PE architecture: $component" }
        } else {
            $machine = if ($target -eq 'linux-amd64') { 62 } else { 183 }
            if ($bytes.Length -lt 20 -or [BitConverter]::ToUInt32($bytes,0) -ne 0x464c457f -or $bytes[4] -ne 2 -or $bytes[5] -ne 1 -or [BitConverter]::ToUInt16($bytes,18) -ne $machine) { throw "Wrong ELF architecture: $target/$component" }
        }
    }
    Write-Output "Verified $target : $($files.Count) files, $($seen.Count) SHA256 entries, source assets and binary architecture match."
}
