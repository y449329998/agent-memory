# agent-memory Windows installer build: go build -> stage -> NSIS -> out\agent-memory-setup-x64.exe
# Usage: powershell -ExecutionPolicy Bypass -File scripts\package.ps1 [-SkipBuild] [-SkipTests]
param(
    [switch]$SkipBuild,
    [switch]$SkipTests
)
$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [Text.Encoding]::UTF8

$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
    $outDir = Join-Path $repo "out"
    $appExe = Join-Path $outDir "agent-memory-app.exe"
    $cliExe = Join-Path $outDir "agent-memory.exe"

    if (-not $SkipBuild) {
        if (-not $SkipTests) {
            & go test ./...
            if ($LASTEXITCODE -ne 0) { throw "go test failed (exit $LASTEXITCODE)" }
        }
        New-Item -ItemType Directory -Force -Path $outDir | Out-Null
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        & go build -trimpath -ldflags "-s -w" -o $cliExe ./cmd/agent-memory
        if ($LASTEXITCODE -ne 0) { throw "build agent-memory failed" }
        & go build -trimpath -ldflags "-s -w -H windowsgui" -o $appExe ./cmd/agent-memory-app
        if ($LASTEXITCODE -ne 0) { throw "build agent-memory-app failed" }
    }
    if (-not (Test-Path $appExe)) { throw "missing build output: $appExe" }
    if (-not (Test-Path $cliExe)) { throw "missing build output: $cliExe" }

    $rootGo = Get-Content '.\internal\cli\root.go' -Raw -Encoding UTF8
    $m = [regex]::Match($rootGo, 'const\s+Version\s*=\s*"([^"]+)"')
    if (-not $m.Success) { throw "cannot parse Version from internal/cli/root.go" }
    $version = $m.Groups[1].Value
    $nums = @([regex]::Matches($version, '\d+') | ForEach-Object { $_.Value })
    while ($nums.Count -lt 4) { $nums += '0' }
    $fileVersion = ($nums[0..3] -join '.')

    $stage = Join-Path $outDir "pkg"
    if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
    New-Item -ItemType Directory -Force -Path (Join-Path $stage 'docs') | Out-Null
    Copy-Item $appExe (Join-Path $stage 'agent-memory-app.exe') -Force
    Copy-Item $cliExe (Join-Path $stage 'agent-memory.exe') -Force
    Copy-Item '.\README.md' (Join-Path $stage 'README.md') -Force
    Copy-Item '.\docs\usage.md' (Join-Path $stage 'docs\usage.md') -Force
    Copy-Item '.\docs\client-guide.md' (Join-Path $stage 'docs\client-guide.md') -Force
    if (Test-Path '.\docs\images\client') {
        Copy-Item '.\docs\images' (Join-Path $stage 'docs\images') -Recurse -Force
    }

    $sizeKb = [int][Math]::Ceiling(((Get-ChildItem $stage -Recurse -File | Measure-Object -Property Length -Sum).Sum) / 1KB)

    $makensis = $null
    $candidates = @(
        (Join-Path $env:ProgramFiles 'NSIS\makensis.exe'),
        (Join-Path ${env:ProgramFiles(x86)} 'NSIS\makensis.exe')
    )
    foreach ($c in $candidates) {
        if ($c -and (Test-Path $c)) { $makensis = $c; break }
    }
    if (-not $makensis) {
        $cache = Join-Path $env:LOCALAPPDATA 'electron-builder\Cache\nsis'
        if (Test-Path $cache) {
            $found = Get-ChildItem $cache -Recurse -Filter makensis.exe -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($found) { $makensis = $found.FullName }
        }
    }
    if (-not $makensis) { throw "makensis.exe not found; install NSIS 3 or keep the electron-builder cache" }

    $outFile = Join-Path $outDir "agent-memory-setup-x64.exe"
    if (Test-Path $outFile) { Remove-Item $outFile -Force }
    $nsi = Join-Path $repo 'installer\agent-memory.nsi'
    $icon = Join-Path $repo 'internal\desktop\assets\app.ico'
    if (-not (Test-Path $icon)) { throw "missing icon: $icon" }

    Write-Host "packaging version=$version fileVersion=$fileVersion payload=${sizeKb}KB"
    try {
        $nsisArgs = @(
            '/V2',
            "/DAPP_VERSION=$version",
            "/DAPP_FILE_VERSION=$fileVersion",
            "/DAPP_SOURCE=$stage",
            "/DAPP_SIZE_KB=$sizeKb",
            "/DOUT_FILE=$outFile",
            "/DAPP_ICON=$icon",
            $nsi
        )
        & $makensis $nsisArgs
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path $outFile)) {
            throw "NSIS failed (exit $LASTEXITCODE)"
        }
    } finally {
        Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
    }

    $mb = [Math]::Round((Get-Item $outFile).Length / 1MB, 1)
    $hash = (Get-FileHash $outFile -Algorithm SHA256).Hash.ToLower()
    Write-Host "installer: $outFile ($mb MB)"
    Write-Host "sha256:    $hash"
} finally {
    Pop-Location
}
