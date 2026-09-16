@echo off
setlocal EnableDelayedExpansion
rem ============================================================
rem  SSH Tunnel Manager - one-shot Windows release build + installer
rem
rem  Usage:
rem    scripts\build_windows.bat [version] [build-name] [build-number]
rem
rem  Steps:
rem    1. flutter build windows --release
rem    2. build the in-process engine library (Go c-shared) and copy it
rem       next to the client exe - that is where the client loads it from.
rem       This is the default engine; without it the client falls back to
rem       the separate daemon, which reintroduces the startup handshake.
rem       Set SKIP_ENGINE=1 to skip it deliberately.
rem    3. go build daemon and copy into the release dir (fallback engine,
rem       and still used by "mark-portable" below)
rem    4. copy ssh-tunnel.example.toml as a sample config
rem    5. run Inno Setup 6 to produce the setup installer (dist\)
rem    6. pack the release dir into a portable zip (dist\)
rem
rem  Prereqs: Flutter / Go / VS Build Tools on PATH.
rem           mingw-w64 gcc for the c-shared engine build
rem           (or drop one at .tools\mingw64\bin\gcc.exe).
rem           Inno Setup 6 for the installer (winget install JRSoftware.InnoSetup)
rem ============================================================

cd /d "%~dp0.."

set "APP_VERSION=%~1"
if "%APP_VERSION%"=="" set "APP_VERSION=1.0.0"
set "BUILD_NAME=%~2"
if "%BUILD_NAME%"=="" for /f "tokens=1 delims=-+" %%V in ("%APP_VERSION%") do set "BUILD_NAME=%%V"
set "BUILD_NUMBER=%~3"
if "%BUILD_NUMBER%"=="" set "BUILD_NUMBER=1"
set "RELEASE_REPOSITORY=%GITHUB_REPOSITORY%"
if "%RELEASE_REPOSITORY%"=="" set "RELEASE_REPOSITORY=byteporter/ssh-tunnel"

set "RELEASE_DIR=client\build\windows\x64\runner\Release"

echo [1/6] Building Flutter client...
pushd client
call flutter build windows --release --build-name "%BUILD_NAME%" --build-number "%BUILD_NUMBER%" || exit /b 1
popd

echo [2/6] Building in-process engine library...
if not exist "%RELEASE_DIR%" mkdir "%RELEASE_DIR%"
if "%SKIP_ENGINE%"=="1" (
  echo   SKIP_ENGINE=1 - skipping; client will fall back to daemon mode.
) else (
  powershell -NoProfile -ExecutionPolicy Bypass -File "scripts\build_engine.ps1" -Version "%APP_VERSION%" || exit /b 1
  copy /y "daemon\bin\ssh-tunnel.dll" "%RELEASE_DIR%\ssh-tunnel.dll" >nul || exit /b 1
)

echo [3/6] Building Go daemon into release dir...
pushd daemon
go build -trimpath -ldflags "-s -w -X main.version=%APP_VERSION% -X main.releaseRepository=%RELEASE_REPOSITORY%" -o "..\%RELEASE_DIR%\ssh-tunnel-daemon.exe" ./cmd/ssh-tunnel || exit /b 1
popd

echo [4/6] Copying sample config...
copy /y "daemon\ssh-tunnel.example.toml" "%RELEASE_DIR%\ssh-tunnel.example.toml" >nul || exit /b 1
"%RELEASE_DIR%\ssh-tunnel-daemon.exe" update mark-portable || exit /b 1

echo [5/6] Building installer with Inno Setup 6...
set "ISCC="
for %%P in (ISCC.exe) do if not defined ISCC set "ISCC=%%~$PATH:P"
if not defined ISCC if exist "%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe" set "ISCC=%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe" set "ISCC=%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%ProgramFiles%\Inno Setup 6\ISCC.exe" set "ISCC=%ProgramFiles%\Inno Setup 6\ISCC.exe"
if not defined ISCC (
  echo   Inno Setup 6 not found - ISCC.exe missing, skipping installer.
  echo   Install with: winget install JRSoftware.InnoSetup
  echo   Release dir is ready: %RELEASE_DIR%
  goto pack_portable
)
"%ISCC%" "/DAppVersion=%APP_VERSION%" "/DAppBuildName=%BUILD_NAME%" "/DAppBuildNumber=%BUILD_NUMBER%" "scripts\installer.iss" || exit /b 1

:pack_portable
echo [6/6] Packing portable zip...
if not exist "dist" mkdir "dist"
powershell -NoProfile -Command "$ErrorActionPreference = 'Stop'; Compress-Archive -Path '%RELEASE_DIR%\*' -DestinationPath 'dist\ssh-tunnel-portable-%APP_VERSION%.zip' -Force" || exit /b 1

echo.
echo Done!
if defined ISCC echo   Installer: dist\ssh-tunnel-setup-%APP_VERSION%.exe
echo   Portable:  dist\ssh-tunnel-portable-%APP_VERSION%.zip
endlocal
