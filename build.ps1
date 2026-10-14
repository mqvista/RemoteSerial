# Build script for rsagent (Windows host).
# Usage:
#   .\build.ps1                      build web + windows/amd64 + linux/arm64
#   .\build.ps1 -Version 0.1.0       stamp a version
#   .\build.ps1 -SkipWeb             skip npm install / vite build
#   .\build.ps1 -Targets windows     only one target (windows | linux-arm64 | linux-amd64)
param(
    [string]$Version = "dev",
    [switch]$SkipWeb,
    [string[]]$Targets = @("windows", "linux-arm64")
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

$matrix = @{
    "windows"     = @{ GOOS = "windows"; GOARCH = "amd64"; Out = "rsagent-windows-amd64.exe" }
    "linux-arm64" = @{ GOOS = "linux";   GOARCH = "arm64"; Out = "rsagent-linux-arm64" }
    "linux-amd64" = @{ GOOS = "linux";   GOARCH = "amd64"; Out = "rsagent-linux-amd64" }
}

foreach ($t in $Targets) {
    if (-not $matrix.ContainsKey($t)) { throw "unknown target: $t" }
    $m = $matrix[$t]
    $out = Join-Path $distDir $m.Out
    Write-Host "==> go build $($m.GOOS)/$($m.GOARCH) -> $out"
    $env:GOOS = $m.GOOS
    $env:GOARCH = $m.GOARCH
    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags $ldflags -o $out ./cmd/rsagent
    if ($LASTEXITCODE -ne 0) { throw "go build failed for $t" }
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
Write-Host "==> done: $distDir"
