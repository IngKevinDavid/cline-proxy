# ==============================================================================
# Installer and Builder for cline-proxy-foxy (Go)
# Author: Ing. Kevin David Condori / Pair Programming Assistant
# Windows PowerShell 5.1+ / PowerShell Core
# ==============================================================================

[CmdletBinding()]
param(
    [switch]$StartService,
    [switch]$ForceRebuild
)

$ErrorActionPreference = "Stop"

function Write-Step {
    param([string]$Message)
    Write-Host "`n[STEP] $Message" -ForegroundColor Cyan
}

function Write-Success {
    param([string]$Message)
    Write-Host "[SUCCESS] $Message" -ForegroundColor Green
}

function Write-Warn {
    param([string]$Message)
    Write-Host "[WARN] $Message" -ForegroundColor Yellow
}

function Write-Err {
    param([string]$Message)
    Write-Host "[ERROR] $Message" -ForegroundColor Red
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
Set-Location $ScriptDir

Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "         cline-proxy-foxy Automated Setup & Builder         " -ForegroundColor Cyan
Write-Host "============================================================" -ForegroundColor Cyan

# ------------------------------------------------------------------------------
# 1. Stop Any Running Instances
# ------------------------------------------------------------------------------
Write-Step "Checking for active cline-proxy processes..."
$RunningProcs = Get-Process -Name "cline-proxy" -ErrorAction SilentlyContinue | Where-Object {
    try { $_.Path -like "$ScriptDir*" } catch { $true }
}
if ($RunningProcs) {
    Write-Warn ("Found running cline-proxy (PID: {0}). Stopping to allow recompilation..." -f ($RunningProcs.Id -join ', '))
    $RunningProcs | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 600
} else {
    Write-Host "No active instances found." -ForegroundColor Gray
}

# ------------------------------------------------------------------------------
# 2. Check Go Environment
# ------------------------------------------------------------------------------
Write-Step "Checking Go environment..."
$GoBin = Join-Path $env:ProgramFiles "Go\bin"
$UserGoBin = Join-Path $env:USERPROFILE "go\bin"
$GopathBin = if ($env:GOPATH) { Join-Path $env:GOPATH "bin" } else { $null }

foreach ($dir in @($GoBin, $UserGoBin, $GopathBin)) {
    if ($dir -and (Test-Path $dir)) {
        if ($env:PATH -notlike "*$dir*") {
            $env:PATH = "$dir;$env:PATH"
        }
    }
}

$GoCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $GoCmd) {
    Write-Err "Go compiler not found in PATH or at '$GoBin'. Please install Go 1.22+."
    exit 1
}
$GoVer = (& go version)
Write-Success "Found Go: $GoVer"

# ------------------------------------------------------------------------------
# 3. Download Dependencies
# ------------------------------------------------------------------------------
Write-Step "Downloading Go module dependencies..."
& go mod download
if ($LASTEXITCODE -ne 0) {
    Write-Err "Failed to download Go modules."
    exit $LASTEXITCODE
}
Write-Success "Go dependencies ready."

# ------------------------------------------------------------------------------
# 4. Compile Binary
# ------------------------------------------------------------------------------
Write-Step "Compiling cline-proxy.exe..."
$OutputBinary = Join-Path $ScriptDir "cline-proxy.exe"

if (-not (Test-Path $OutputBinary) -or $ForceRebuild) {
    & go build -trimpath -ldflags="-s -w" -o $OutputBinary .
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Compilation failed."
        exit $LASTEXITCODE
    }
}

if (-not (Test-Path $OutputBinary)) {
    Write-Err "Output binary not found at $OutputBinary"
    exit 1
}

$BinarySize = (Get-Item $OutputBinary).Length / 1MB
Write-Success ("Compiled binary verified: {0} ({1:N2} MB)" -f $OutputBinary, $BinarySize)

# ------------------------------------------------------------------------------
# 5. Ensure Data Directory
# ------------------------------------------------------------------------------
$DataDir = Join-Path $ScriptDir "data"
if (-not (Test-Path $DataDir)) {
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
}

# ------------------------------------------------------------------------------
# 6. Start Service (if requested)
# ------------------------------------------------------------------------------
if ($StartService) {
    Write-Step "Launching cline-proxy via server-proxy.ps1..."
    & .\server-proxy.ps1 -Background
}

Write-Host "`n============================================================" -ForegroundColor Cyan
Write-Host "                     SETUP FINISHED                         " -ForegroundColor Cyan
Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "Binary:         $OutputBinary"
Write-Host "Data Dir:       $DataDir"
Write-Host "Start Server:   .\server-proxy.ps1"
Write-Host "Background:     .\server-proxy.ps1 -Background"
Write-Host "Stop Server:    .\server-proxy.ps1 -Stop"
Write-Host "Check Status:   .\server-proxy.ps1 -Status"
Write-Host "Open Admin:     .\server-proxy.ps1 -OpenBrowser"
Write-Host "Admin Panel:    http://127.0.0.1:3457/admin/"
Write-Host "API Endpoint:   http://127.0.0.1:3457/v1"
Write-Host "============================================================`n"
