# PowerShell helper to run user-space Tor SOCKS5 proxy on 127.0.0.1:9052
# No administrator privileges required. Self-contained within cline-proxy.

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$torExe = [System.IO.Path]::GetFullPath((Join-Path $scriptDir "..\tools\tor\tor.exe"))
if (-not (Test-Path $torExe)) {
    # Fallback to current working directory
    $torExe = [System.IO.Path]::GetFullPath("tools\tor\tor.exe")
}
if (-not (Test-Path $torExe)) {
    Write-Error "Tor binary not found at $torExe"
    exit 1
}

$conn = Get-NetTCPConnection -LocalPort 9052 -ErrorAction SilentlyContinue
if ($conn) {
    Write-Host "[tor] Tor is already listening on 127.0.0.1:9052" -ForegroundColor Green
    exit 0
}

$dataDir = Join-Path $env:TEMP "cline-proxy-tor-data"
if (-not (Test-Path $dataDir)) {
    New-Item -ItemType Directory -Path $dataDir | Out-Null
}

Write-Host "[tor] Starting Tor SOCKS5 proxy on 127.0.0.1:9052 (ControlPort 9053)..." -ForegroundColor Cyan
Start-Process -FilePath $torExe -ArgumentList @(
    "--SocksPort", "9052",
    "--ControlPort", "9053",
    "--CookieAuthentication", "0",
    "--DataDirectory", $dataDir,
    "--RunAsDaemon", "0"
) -WindowStyle Hidden

Start-Sleep -Seconds 3
$check = Get-NetTCPConnection -LocalPort 9052 -ErrorAction SilentlyContinue
if ($check) {
    Write-Host "[tor] Tor SOCKS5 proxy is ready on 127.0.0.1:9052!" -ForegroundColor Green
} else {
    Write-Host "[tor] Tor is starting in background, circuit building in progress." -ForegroundColor Yellow
}
