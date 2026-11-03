@echo off
chcp 65001 >nul
title cline-proxy-foxy

rem ==============================================================================
rem Objetivo:     Lanzador y Gestor de Servicio para cline-proxy-foxy en Windows
rem Autor:        Ing. Kevin David Condori
rem Insumos:      cline-proxy.exe, data/
rem Productos:    Servicio proxy en http://127.0.0.1:3457 (/v1, /admin)
rem Dependencias: Windows CMD / PowerShell, curl.exe
rem ==============================================================================

set "SCRIPT_DIR=%~dp0"
set "ROOT_DIR=%SCRIPT_DIR:~0,-1%"
cd /d "%ROOT_DIR%"

set "BINARY=%ROOT_DIR%\cline-proxy.exe"
set "DATA_DIR=%ROOT_DIR%\data"
set "PORT=3457"
set "HOST=127.0.0.1"
set "HEALTH_URL=http://%HOST%:%PORT%/health"
set "ADMIN_URL=http://%HOST%:%PORT%/admin/"
set "API_URL=http://%HOST%:%PORT%/v1"

rem Auto-configurar OpenCode Zen Harvester si opencode CLI está instalado
if "%ZEN_HARVEST_BIN%"=="" (
    if exist "%APPDATA%\npm\node_modules\@opencode\cli\bin\opencode.exe" set "ZEN_HARVEST_BIN=%APPDATA%\npm\node_modules\@opencode\cli\bin\opencode.exe"
    if "%ZEN_HARVEST_BIN%"=="" if exist "%APPDATA%\npm\node_modules\opencode-ai\bin\opencode.exe" set "ZEN_HARVEST_BIN=%APPDATA%\npm\node_modules\opencode-ai\bin\opencode.exe"
    if "%ZEN_HARVEST_BIN%"=="" if exist "%LOCALAPPDATA%\Programs\opencode\opencode.exe" set "ZEN_HARVEST_BIN=%LOCALAPPDATA%\Programs\opencode\opencode.exe"
)
if "%ZEN_HARVEST_HOME%"=="" set "ZEN_HARVEST_HOME=%DATA_DIR%\.opencode-home"

if not exist "%DATA_DIR%" mkdir "%DATA_DIR%"

rem Parsear comando/argumento
set "CMD=%~1"
if /i "%CMD%"=="stop" goto :do_stop
if /i "%CMD%"=="-stop" goto :do_stop
if /i "%CMD%"=="--stop" goto :do_stop
if /i "%CMD%"=="status" goto :do_status
if /i "%CMD%"=="-status" goto :do_status
if /i "%CMD%"=="--status" goto :do_status
if /i "%CMD%"=="bg" goto :do_background
if /i "%CMD%"=="background" goto :do_background
if /i "%CMD%"=="-background" goto :do_background
if /i "%CMD%"=="--background" goto :do_background
if /i "%CMD%"=="help" goto :do_help
if /i "%CMD%"=="-help" goto :do_help
if /i "%CMD%"=="--help" goto :do_help
if /i "%CMD%"=="-h" goto :do_help

goto :do_foreground

:do_stop
echo [INFO] Deteniendo procesos cline-proxy...
taskkill /F /T /IM cline-proxy.exe >nul 2>&1
if not errorlevel 1 (
    echo [SUCCESS] Procesos cline-proxy detenidos exitosamente.
) else (
    echo [INFO] No se encontraron procesos activos de cline-proxy.
)
exit /b 0

:do_status
echo ============================================================
echo                 cline-proxy-foxy STATUS                     
echo ============================================================
curl.exe -s -m 2 "%HEALTH_URL%" >nul 2>&1
if not errorlevel 1 goto :status_running

tasklist /FI "IMAGENAME eq cline-proxy.exe" 2>nul | find /I "cline-proxy.exe" >nul
if not errorlevel 1 goto :status_unresponsive

echo [ESTADO]  STOPPED (No hay procesos activos)
goto :status_end

:status_running
echo [ESTADO]  RUNNING (Saludable / Respondiendo)
echo [ADMIN]   %ADMIN_URL%
echo [GATEWAY] %API_URL%
for /f "delims=" %%a in ('curl.exe -s "%HEALTH_URL%"') do echo [INFO]    %%a
goto :status_end

:status_unresponsive
echo [ESTADO]  PROCESO DETECTADO (Pero no responde en %HEALTH_URL%)
goto :status_end

:status_end
echo ============================================================
exit /b 0

:do_background
curl.exe -s -m 2 "%HEALTH_URL%" >nul 2>&1
if not errorlevel 1 (
    echo [INFO] cline-proxy-foxy ya está corriendo y respondiendo en %ADMIN_URL%
    exit /b 0
)

if exist "%BINARY%" goto :bg_start
echo [WARN] Binario no encontrado en %BINARY%. Compilando con Go...
where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go no está instalado o no está en el PATH.
    exit /b 1
)
go build -o cline-proxy.exe .
if errorlevel 1 (
    echo [ERROR] Falló la compilación de cline-proxy.exe.
    exit /b 1
)

:bg_start
echo ============================================================
echo               INICIANDO cline-proxy-foxy                    
echo ============================================================
echo Binario:      %BINARY%
echo Host/Puerto:  %HOST%:%PORT%
echo Modo:         Segundo plano (Background)
echo Admin Panel:  %ADMIN_URL%
echo API Gateway:  %API_URL%
echo ============================================================

powershell -NoProfile -Command "$p = [wmiclass]'Win32_Process'; [void]$p.Create('\"%BINARY%\" -host %HOST% -port %PORT%', '%ROOT_DIR%', $null)"

echo [INFO] Esperando disponibilidad del servicio...
for /L %%i in (1,1,15) do (
    ping -n 2 127.0.0.1 >nul
    curl.exe -s -m 2 "%HEALTH_URL%" >nul 2>&1
    if not errorlevel 1 goto :bg_ready
)

echo.
echo [WARN] El proxy se inició pero aún no responde al health check en %HEALTH_URL%.
exit /b 0

:bg_ready
echo.
echo [SUCCESS] cline-proxy-foxy está activo y saludable.
echo Admin Panel:  %ADMIN_URL%
echo API Gateway:  %API_URL%
echo Para detener: server-proxy.bat stop
exit /b 0

:do_foreground
curl.exe -s -m 2 "%HEALTH_URL%" >nul 2>&1
if not errorlevel 1 (
    echo ============================================================
    echo           cline-proxy-foxy YA ESTÁ EN EJECUCIÓN             
    echo ============================================================
    echo Admin Panel:  %ADMIN_URL%
    echo API Gateway:  %API_URL%
    echo.
    echo Para detener:        server-proxy.bat stop
    echo Para ver estado:     server-proxy.bat status
    echo En segundo plano:    server-proxy.bat bg
    echo ============================================================
    exit /b 0
)

if exist "%BINARY%" goto :fg_start
echo [WARN] Binario no encontrado en %BINARY%. Compilando con Go...
where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go no está instalado o no está en el PATH.
    exit /b 1
)
go build -o cline-proxy.exe .
if errorlevel 1 (
    echo [ERROR] Falló la compilación de cline-proxy.exe.
    exit /b 1
)

:fg_start
echo ============================================================
echo               INICIANDO cline-proxy-foxy                    
echo ============================================================
echo Binario:      %BINARY%
echo Host/Puerto:  %HOST%:%PORT%
echo Admin Panel:  %ADMIN_URL%
echo API Gateway:  %API_URL%
echo ============================================================
echo Presiona Ctrl+C para detener el proxy.
echo.

"%BINARY%" -host %HOST% -port %PORT% %*
exit /b %ERRORLEVEL%

:do_help
echo Uso: server-proxy.bat [comando]
echo.
echo Comandos:
echo   (sin argumentos)  Inicia el proxy en primer plano (consola activa)
echo   bg, background    Inicia el proxy en segundo plano (oculto)
echo   stop              Detiene los procesos de cline-proxy
echo   status            Muestra el estado del servicio y health check
echo   help              Muestra esta ayuda
exit /b 0