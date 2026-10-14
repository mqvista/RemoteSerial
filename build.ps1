# Build script for rsagent (Windows host).
# Usage:
#   .\build.ps1                      build web + windows/amd64 + linux/arm64
#   .\build.ps1 -Version 0.1.0       stamp a version
#   .\build.ps1 -SkipWeb             skip npm install / vite build
#   .\build.ps1 -Targets windows     only one target (windows | linux-arm64 | linux-amd64)
#   .\build.ps1 -Installer           also build the Inno Setup installer (needs ISCC.exe; see Find-ISCC)
#   .\build.ps1 -Manifest -BaseURL https://.../v0.4.0   also write dist/manifest.json + SHA256SUMS
param(
    [string]$Version = "dev",
    [switch]$SkipWeb,
    [string[]]$Targets = @("windows", "linux-arm64"),
    [switch]$Installer,
    [switch]$Manifest,
    [string]$BaseURL = ""
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$distDir = Join-Path $root "dist"
New-Item -ItemType Directory -Force $distDir | Out-Null

if (-not $SkipWeb) {
    Write-Host "==> building web"
    Push-Location (Join-Path $root "web")
    try {
        if (Test-Path "package-lock.json") { npm ci } else { npm install }
        if ($LASTEXITCODE -ne 0) { throw "npm install failed" }
        $assets = Join-Path $root "internal\web\dist\assets"
        if (Test-Path $assets) { Remove-Item -Recurse -Force $assets }
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "vite build failed" }
    } finally {
        Pop-Location
    }
}

$commit = "none"
try { $c = (git -C $root rev-parse --short HEAD 2>$null); if ($c) { $commit = $c.Trim() } } catch {}
$date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ldflags = "-s -w " +
    "-X rsagent/internal/buildinfo.Version=$Version " +
    "-X rsagent/internal/buildinfo.Commit=$commit " +
    "-X rsagent/internal/buildinfo.Date=$date"

# The Windows release uses the GUI subsystem so double-clicking shows no console window;
# non-ui subcommands re-attach to the parent console when run from a terminal.
$matrix = @{
    "windows"     = @{ GOOS = "windows"; GOARCH = "amd64"; Out = "rsagent-windows-amd64.exe"; Extra = "-H windowsgui" }
    "linux-arm64" = @{ GOOS = "linux";   GOARCH = "arm64"; Out = "rsagent-linux-arm64";       Extra = "" }
    "linux-amd64" = @{ GOOS = "linux";   GOARCH = "amd64"; Out = "rsagent-linux-amd64";       Extra = "" }
}

foreach ($t in $Targets) {
    if (-not $matrix.ContainsKey($t)) { throw "unknown target: $t" }
    $m = $matrix[$t]
    $out = Join-Path $distDir $m.Out
    Write-Host "==> go build $($m.GOOS)/$($m.GOARCH) -> $out"
    $env:GOOS = $m.GOOS
    $env:GOARCH = $m.GOARCH
    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags "$ldflags $($m.Extra)" -o $out ./cmd/rsagent
    if ($LASTEXITCODE -ne 0) { throw "go build failed for $t" }
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

# Inno Setup compiler: env ISCC, standard install dirs, scoop, or a portable extraction (innoextract) in INNO_SETUP_DIR.
function Find-ISCC {
    $candidates = @()
    if ($env:ISCC) { $candidates += $env:ISCC }
    if ($env:INNO_SETUP_DIR) { $candidates += (Join-Path $env:INNO_SETUP_DIR "ISCC.exe"); $candidates += (Join-Path $env:INNO_SETUP_DIR "app\ISCC.exe") }
    $candidates += "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe"
    $candidates += "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
    $candidates += "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe"
    $candidates += "$HOME\scoop\apps\inno-setup\current\ISCC.exe"
    foreach ($c in $candidates) { if ($c -and (Test-Path $c)) { return $c } }
    $cmd = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

if ($Installer) {
    if ($Targets -notcontains "windows") { throw "-Installer needs the windows target" }
    $iscc = Find-ISCC
    if (-not $iscc) { throw "ISCC.exe not found: install Inno Setup 6, or set ISCC / INNO_SETUP_DIR" }
    Write-Host "==> installer via $iscc"
    & $iscc /Q "/DAppVersion=$Version" (Join-Path $root "installer\rsagent.iss")
    if ($LASTEXITCODE -ne 0) { throw "ISCC failed" }
}

if ($Manifest) {
    if (-not $BaseURL) { $BaseURL = "https://github.com/mqvista/RemoteSerial/releases/download/v$Version" }
    Write-Host "==> manifest"
    go run ./tools/mkmanifest -version $Version -dir $distDir -base $BaseURL
    if ($LASTEXITCODE -ne 0) { throw "mkmanifest failed" }
}
Write-Host "==> done: $distDir"
