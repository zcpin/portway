@echo off
setlocal EnableDelayedExpansion
rem ============================================================
rem  SSH Tunnel Manager - one-shot Windows release build + installer
rem
rem  Usage:
rem    scripts\build_windows.bat [version]
rem
rem  Steps:
rem    1. flutter build windows --release
rem    2. go build daemon and copy into the release dir (next to the
rem       client exe, which is where the client auto-launches it from)
rem    3. copy ssh-tunnel.example.toml as a sample config
rem    4. run Inno Setup 6 to produce the setup installer (dist\)
rem    5. pack the release dir into a portable zip (dist\)
rem
rem  Prereqs: Flutter / Go / VS Build Tools on PATH.
rem           Inno Setup 6 for the installer (winget install JRSoftware.InnoSetup)
rem ============================================================

cd /d "%~dp0.."

set "APP_VERSION=%~1"
if "%APP_VERSION%"=="" set "APP_VERSION=1.0.0"

set "RELEASE_DIR=client\build\windows\x64\runner\Release"

echo [1/4] Building Flutter client...
pushd client
call flutter build windows --release || exit /b 1
popd

echo [2/4] Building Go daemon into release dir...
if not exist "%RELEASE_DIR%" mkdir "%RELEASE_DIR%"
pushd daemon
go build -trimpath -ldflags "-s -w -X main.version=%APP_VERSION%" -o "..\%RELEASE_DIR%\ssh-tunnel-daemon.exe" ./cmd/ssh-tunnel || exit /b 1
popd

echo [3/4] Copying sample config...
copy /y "daemon\ssh-tunnel.example.toml" "%RELEASE_DIR%\ssh-tunnel.example.toml" >nul

echo [4/4] Building installer with Inno Setup 6...
set "ISCC="
for %%P in (ISCC.exe) do if not defined ISCC set "ISCC=%%~$P:P"
if not defined ISCC if exist "%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe" set "ISCC=%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe" set "ISCC=%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%ProgramFiles%\Inno Setup 6\ISCC.exe" set "ISCC=%ProgramFiles%\Inno Setup 6\ISCC.exe"
if not defined ISCC (
  echo   Inno Setup 6 not found - ISCC.exe missing, skipping installer.
  echo   Install with: winget install JRSoftware.InnoSetup
  echo   Release dir is ready: %RELEASE_DIR%
  exit /b 0
)
"%ISCC%" "/DAppVersion=%APP_VERSION%" "scripts\installer.iss" || exit /b 1

echo [5/5] Packing portable zip...
if not exist "dist" mkdir "dist"
powershell -NoProfile -Command "Compress-Archive -Path '%RELEASE_DIR%\*' -DestinationPath 'dist\ssh-tunnel-portable-%APP_VERSION%.zip' -Force" || exit /b 1

echo.
echo Done!
echo   Installer: dist\ssh-tunnel-setup-%APP_VERSION%.exe
echo   Portable:  dist\ssh-tunnel-portable-%APP_VERSION%.zip
endlocal
