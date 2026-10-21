# Dot-source this script before project commands: . .\scripts\Use-DevEnvironment.ps1
$ErrorActionPreference = 'Stop'
$goBin = 'E:\SDK\Go\go1.27.3\go\bin'
$gitBin = 'E:\SDK\Git\cmd'
foreach ($directory in @($goBin, $gitBin)) {
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        throw "Required SDK directory is missing: $directory"
    }
    if ($directory -notin ($env:Path -split ';')) { $env:Path += ";$directory" }
}
$env:GOPATH = 'E:\SDK\Go\workspace'
$env:GOMODCACHE = 'E:\SDK\Caches\proxypanel\go-mod'
$env:GOCACHE = 'E:\SDK\Caches\proxypanel\go-build'
$env:GOTOOLCHAIN = 'local'
$env:NPM_CONFIG_CACHE = 'E:\SDK\Caches\proxypanel\npm'
$env:PLAYWRIGHT_BROWSERS_PATH = 'E:\SDK\Caches\proxypanel\playwright'
$env:XRAY_BIN = 'E:\SDK\Xray\v26.3.27\xray.exe'
foreach ($directory in @($env:GOPATH, $env:GOMODCACHE, $env:GOCACHE, $env:NPM_CONFIG_CACHE, $env:PLAYWRIGHT_BROWSERS_PATH)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
}
$temporaryRoot = if ($env:PI_SCRATCH_DIR) { $env:PI_SCRATCH_DIR } else { 'E:\SDK\Caches\proxypanel\temp' }
New-Item -ItemType Directory -Path $temporaryRoot -Force | Out-Null
$env:TEMP = $temporaryRoot
$env:TMP = $temporaryRoot
$env:GOTMPDIR = $temporaryRoot
