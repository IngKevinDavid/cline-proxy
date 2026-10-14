# ==============================================================================
# Runner and Service Manager for cline-proxy-foxy
# Author: Ing. Kevin David Condori / Pair Programming Assistant
# Windows PowerShell 5.1+ / PowerShell Core
# ==============================================================================

[CmdletBinding()]
param(
    [switch]$Background,
    [switch]$Stop,
    [switch]$Status,
    [switch]$OpenBrowser,
    [string]$BindHost = "127.0.0.1",
    [int]$Port = 3457
)

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
Set-Location $ScriptDir

$BinaryPath = Join-Path $ScriptDir "cline-proxy.exe"
$DataDir    = Join-Path $ScriptDir "data"
$LogFile    = Join-Path $DataDir "start.log"
$HealthUri  = "http://127.0.0.1:$Port/health"
$AdminUrl   = "http://127.0.0.1:$Port/admin/"

if (-not (Test-Path $DataDir)) {
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
}

# Auto-configure OpenCode Zen Harvester on Windows if opencode is installed
if (-not $env:ZEN_HARVEST_BIN) {
    $candidates = @(
        (Join-Path $env:APPDATA "npm\node_modules\@opencode\cli\bin\opencode.exe"),
        (Join-Path $env:APPDATA "npm\node_modules\opencode-ai\bin\opencode.exe"),
        (Join-Path $env:LOCALAPPDATA "Programs\opencode\opencode.exe")
    )
    foreach ($cand in $candidates) {
        if (Test-Path $cand) {
            $env:ZEN_HARVEST_BIN = $cand
            break
        }
    }
}
if (-not $env:ZEN_HARVEST_HOME) {
    $env:ZEN_HARVEST_HOME = Join-Path $DataDir ".opencode-home"
}

function Test-ServerHealthy {
    param([int]$TimeoutSec = 2)
    try {
        $res = Invoke-RestMethod -Uri $HealthUri -TimeoutSec $TimeoutSec -ErrorAction Stop
        return ($res -and ($res.status -eq "ok" -or $res.ok -eq $true))
    } catch {
        return $false
    }
}

function Get-ProxyProcesses {
    $all = Get-Process -Name "cline-proxy" -ErrorAction SilentlyContinue
    $matching = @()
    foreach ($p in $all) {
        try {
            if ($p.Path -and $p.Path -like "$ScriptDir*") {
                $matching += $p
            }
        } catch {
            $matching += $p
        }
    }
    return $matching
}

# ------------------------------------------------------------------------------
# Action: Stop
# ------------------------------------------------------------------------------
if ($Stop) {
    Write-Host "[INFO] Stopping any running cline-proxy processes..." -ForegroundColor Cyan
    $procs = Get-ProxyProcesses
    if ($procs) {
        foreach ($p in $procs) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
            Write-Host "[SUCCESS] Terminated PID $($p.Id) ($($p.ProcessName))." -ForegroundColor Green
        }
    } else {
        Write-Host "[INFO] No running processes found for cline-proxy-foxy." -ForegroundColor Gray
    }
    exit 0
}

# ------------------------------------------------------------------------------
# Action: Status
# ------------------------------------------------------------------------------
if ($Status) {
    Write-Host "============================================================" -ForegroundColor Cyan
    Write-Host "                cline-proxy-foxy STATUS                     " -ForegroundColor Cyan
    Write-Host "============================================================" -ForegroundColor Cyan
    $procs = Get-ProxyProcesses
    $isHealthy = Test-ServerHealthy

    if ($isHealthy) {
        Write-Host "Service State: RUNNING (Healthy)" -ForegroundColor Green
        Write-Host "Health Check:  $HealthUri" -ForegroundColor Green
        Write-Host "Admin Panel:   $AdminUrl" -ForegroundColor Cyan
        Write-Host "API Gateway:   http://127.0.0.1:$Port/v1" -ForegroundColor Cyan
        try {
            $info = Invoke-RestMethod -Uri $HealthUri -TimeoutSec 2
            Write-Host "Active Accts:  $($info.activeAccounts)"
            Write-Host "Version:       $($info.version)"
        } catch {}
    } else {
        Write-Host "Service State: STOPPED" -ForegroundColor Red
        if ($procs) {
            Write-Host "Warning: Process detected (PID: $($procs.Id -join ', ')) but not responding on $HealthUri" -ForegroundColor Yellow
        }
    }

    if ($procs) {
        Write-Host "Processes:     $($procs.Count) instance(s) running (PID: $($procs.Id -join ', '))"
    }
    Write-Host "Data Dir:      $DataDir"
    Write-Host "Log File:      $LogFile"
    Write-Host "============================================================" -ForegroundColor Cyan
    exit 0
}

# ------------------------------------------------------------------------------
# Check if Already Running
# ------------------------------------------------------------------------------
if (Test-ServerHealthy) {
    $procs = Get-ProxyProcesses
    Write-Host "============================================================" -ForegroundColor Cyan
    Write-Host "           cline-proxy-foxy IS ALREADY RUNNING              " -ForegroundColor Green
    Write-Host "============================================================" -ForegroundColor Cyan
    Write-Host "Active Endpoint: http://127.0.0.1:$Port" -ForegroundColor Green
    Write-Host "Admin Panel:     $AdminUrl" -ForegroundColor Cyan
    Write-Host "API Gateway:     http://127.0.0.1:$Port/v1" -ForegroundColor Cyan
    if ($procs) {
        Write-Host "Processes:       $($procs.Count) running (PID: $($procs.Id -join ', '))"
    }
    Write-Host "`nTo stop:           .\server-proxy.ps1 -Stop" -ForegroundColor Gray
    Write-Host "To check status:   .\server-proxy.ps1 -Status" -ForegroundColor Gray
    Write-Host "Open Admin Panel:  .\server-proxy.ps1 -OpenBrowser" -ForegroundColor Gray
    Write-Host "============================================================`n" -ForegroundColor Cyan

    if ($OpenBrowser) {
        Start-Process $AdminUrl
    }
    exit 0
}

# ------------------------------------------------------------------------------
# Ensure Binary Exists
# ------------------------------------------------------------------------------
if (-not (Test-Path $BinaryPath)) {
    Write-Host "[WARN] Binary not found at $BinaryPath. Compiling first..." -ForegroundColor Yellow
    & .\install.ps1
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $BinaryPath)) {
        Write-Host "[ERROR] Compilation failed. Cannot start server." -ForegroundColor Red
        exit 1
    }
}

# ------------------------------------------------------------------------------
# Launch Server
# ------------------------------------------------------------------------------
Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "              STARTING cline-proxy-foxy                     " -ForegroundColor Cyan
Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "Binary:       $BinaryPath"
Write-Host "Host/Port:    $($BindHost):$Port"
Write-Host "Data Dir:     $DataDir"
Write-Host "Admin Panel:  $AdminUrl"
Write-Host "API Endpoint: http://127.0.0.1:$Port/v1"
Write-Host "============================================================" -ForegroundColor Cyan

if ($Background) {
    Write-Host "[INFO] Starting process in detached background..." -ForegroundColor Cyan
    $fullCmd = "`"$BinaryPath`" -host $BindHost -port $Port"
    $wmiRes = ([wmiclass]"Win32_Process").Create($fullCmd, $ScriptDir, $null)
    if ($wmiRes.ReturnValue -ne 0) {
        Write-Host "[ERROR] Failed to launch process via WMI (Code: $($wmiRes.ReturnValue)). Falling back to Start-Process..." -ForegroundColor Yellow
        $p = Start-Process -FilePath $BinaryPath `
                           -ArgumentList "-host", "$BindHost", "-port", "$Port" `
                           -WorkingDirectory $ScriptDir `
                           -WindowStyle Hidden `
                           -PassThru
        $procId = $p.Id
    } else {
        $procId = $wmiRes.ProcessId
    }

    Write-Host "[INFO] Process started with PID $procId. Waiting for readiness..." -ForegroundColor Cyan

    $maxWaitSec = 15
    $ready = $false
    for ($i = 0; $i -lt $maxWaitSec; $i++) {
        Start-Sleep -Seconds 1
        if (Test-ServerHealthy) {
            $ready = $true
            break
        }
    }

    if ($ready) {
        Write-Host "`n[SUCCESS] cline-proxy-foxy is healthy and ready!" -ForegroundColor Green
        Write-Host "Admin Panel:     $AdminUrl" -ForegroundColor Green
        Write-Host "API Gateway:     http://127.0.0.1:$Port/v1" -ForegroundColor Green
        Write-Host "Logs output:     $LogFile" -ForegroundColor Gray
        Write-Host "To stop service: .\server-proxy.ps1 -Stop`n" -ForegroundColor Gray
        if ($OpenBrowser) {
            Start-Process $AdminUrl
        }
    } else {
        Write-Host "[WARN] Health check did not respond within $maxWaitSec seconds." -ForegroundColor Yellow
        Write-Host "Check logs at: $LogFile" -ForegroundColor Yellow
    }
} else {
    Write-Host "[INFO] Running in foreground (Press Ctrl+C to terminate)...`n" -ForegroundColor Yellow
    if ($OpenBrowser) {
        Start-Process $AdminUrl
    }
    & $BinaryPath -host $BindHost -port $Port
}
