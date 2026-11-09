# Rogue Signal Windows Build & Packaging Guide (PowerShell)
# This script compiles RandomX, core-rogue.exe, and generates Tauri installers (NSIS/MSI) on Windows.

param (
    [switch]$SkipTauri = $false
)

$ErrorActionPreference = "Stop"

Write-Host "=== [Rogue Signal] Windows Production Build ===" -ForegroundColor Cyan

# 1. Verify Prerequisites
$Prereqs = @("gcc", "g++", "cmake", "go", "cargo", "npm")
foreach ($bin in $Prereqs) {
    if (-not (Get-Command $bin -ErrorAction SilentlyContinue)) {
        Write-Error "Missing required dependency: $bin. Ensure MSYS2 MinGW-w64, Go, Rust, and Node.js are installed and in PATH."
    }
}

$ScriptDir = $PSScriptRoot
$CoreDir = (Get-Item $ScriptDir).Parent.FullName
$WorkspaceRoot = (Get-Item $CoreDir).Parent.FullName
$DesktopDir = Join-Path $WorkspaceRoot "rogue-desktop-client"

# 2. Compile librandomx.a
Write-Host "-> Compiling RandomX static library (C++)..." -ForegroundColor Yellow
$RandomxSrc = Join-Path $CoreDir "consensus\randomx\randomx-src"
$RandomxBuild = Join-Path $RandomxSrc "build"

if (-not (Test-Path (Join-Path $RandomxSrc "CMakeLists.txt"))) {
    git -C $CoreDir submodule update --init --recursive consensus/randomx/randomx-src
}

New-Item -ItemType Directory -Force -Path $RandomxBuild | Out-Null
Set-Location $RandomxBuild
cmake -G "MinGW Makefiles" -DARCH=native -DCMAKE_BUILD_TYPE=Release ..
mingw32-make

$LibDest = Join-Path $CoreDir "consensus\randomx\lib"
New-Item -ItemType Directory -Force -Path $LibDest | Out-Null
Copy-Item (Join-Path $RandomxBuild "librandomx.a") (Join-Path $LibDest "librandomx.a") -Force
Write-Host "-> librandomx.a built successfully." -ForegroundColor Green

# 3. Compile core-rogue.exe with CGO
Write-Host "-> Compiling core-rogue.exe with CGO..." -ForegroundColor Yellow
Set-Location $CoreDir
$env:CGO_ENABLED = "1"
go run build/ci.go install ./cmd/geth
Copy-Item "build\bin\geth.exe" "build\bin\core-rogue.exe" -Force

# Verify executable
& "build\bin\core-rogue.exe" version

# 4. Copy core-rogue.exe as Tauri Sidecar (if desktop directory exists)
if (Test-Path $DesktopDir) {
    $SidecarDir = Join-Path $DesktopDir "src-tauri\bin"
    New-Item -ItemType Directory -Force -Path $SidecarDir | Out-Null
    Copy-Item "build\bin\core-rogue.exe" (Join-Path $SidecarDir "core-rogue-x86_64-pc-windows-msvc.exe") -Force
    Copy-Item "build\bin\core-rogue.exe" (Join-Path $SidecarDir "core-rogue.exe") -Force
}

# 5. Build Desktop Installer
if ((-not $SkipTauri) -and (Test-Path $DesktopDir)) {
    Write-Host "-> Building Rogue Desktop installers (NSIS/MSI)..." -ForegroundColor Yellow
    Set-Location $DesktopDir
    npm install
    npm run build
    npx tauri build
    Write-Host "-> Windows bundle completed in src-tauri\target\release\bundle\" -ForegroundColor Green
}

Write-Host "=== Windows build finished successfully! ===" -ForegroundColor Green
