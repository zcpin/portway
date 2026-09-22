# ============================================================
#  构建「进程内引擎」动态库（Go -buildmode=c-shared）
#
#  产物：daemon/bin/portway.dll      (Windows)
#        daemon/bin/libportway.dylib (macOS)
#        daemon/bin/libportway.so    (Linux)
#
#  客户端启动时会在自己的可执行文件同目录查找这个库；开发时也可以
#  用环境变量直接指定：
#      $env:PORTWAY_EMBEDDED_LIB = 'C:\path\to\portway.dll'
#
#  用法：
#      pwsh scripts/build_engine.ps1 [-Version 1.2.3] [-Toolchain <gcc 路径>]
#
#  c-shared 需要 C 工具链（CGO）。Windows 上需要 mingw-w64 的 gcc，
#  没有时用 -Toolchain 指定，或把 gcc 放进 PATH。
# ============================================================
[CmdletBinding()]
param(
    [string]$Version = 'dev',
    [string]$Toolchain = ''
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$daemonDir = Join-Path $repoRoot 'daemon'
$binDir = Join-Path $daemonDir 'bin'

# 不要用 $IsWindows / $IsMacOS：它们只在 PowerShell 6+ 存在，
# Windows PowerShell 5.1 下是未定义变量。
function Get-HostOS {
    if ($env:OS -eq 'Windows_NT') { return 'windows' }
    $uname = ''
    try { $uname = (& uname 2>$null) } catch { $uname = '' }
    if ($uname -eq 'Darwin') { return 'darwin' }
    return 'linux'
}

# ---------- 1. 目标平台与产物名 ----------
switch (Get-HostOS) {
    'windows' { $outName = 'portway.dll' }
    'darwin' { $outName = 'libportway.dylib' }
    default { $outName = 'libportway.so' }
}

$outPath = Join-Path $binDir $outName

# ---------- 2. 找 C 工具链 ----------
# 仓库内的 .tools 目录是给沙箱 / CI 用的本地工具链，优先于 PATH。
function Resolve-CCompiler {
    param([string]$Explicit, [string]$OS)

    if ($Explicit) {
        if (-not (Test-Path -LiteralPath $Explicit)) {
            throw "指定的工具链不存在：$Explicit"
        }
        return (Resolve-Path -LiteralPath $Explicit).Path
    }

    $names = if ($OS -eq 'windows') { @('gcc.exe') } else { @('gcc', 'clang') }
    $candidates = @()
    if ($OS -eq 'windows') {
        $candidates += (Join-Path $repoRoot '.tools\mingw64\bin\gcc.exe')
    }
    foreach ($name in $names) {
        $found = Get-Command $name -ErrorAction SilentlyContinue |
            Select-Object -First 1 -ExpandProperty Source
        if ($found) { $candidates += $found }
    }

    foreach ($candidate in $candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { return $candidate }
    }
    return $null
}

$hostOS = Get-HostOS
$compiler = Resolve-CCompiler -Explicit $Toolchain -OS $hostOS
if (-not $compiler) {
    throw @"
未找到 C 编译器，c-shared 需要 CGO。
  Windows: 安装 mingw-w64（winlibs / MSYS2），或解压到 .tools\mingw64\bin\gcc.exe
  macOS:   xcode-select --install
  Linux:   apt install build-essential
也可用 -Toolchain <gcc 路径> 显式指定。
"@
}

# ---------- 3. 构建 ----------
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

$env:CGO_ENABLED = '1'
$env:CC = $compiler
# mingw 的 gcc 依赖同目录的 dll / 工具，必须并进 PATH，否则链接阶段找不到。
$env:PATH = "$(Split-Path -Parent $compiler)$([IO.Path]::PathSeparator)$env:PATH"

$ldflags = "-s -w -X github.com/byteporter/portway/internal/embedded.Version=$Version"

Write-Host "[engine] 目标平台 : $hostOS"
Write-Host "[engine] C 编译器 : $compiler"
Write-Host "[engine] 版本     : $Version"

$LASTEXITCODE = 0
Push-Location $daemonDir
try {
    & go build -trimpath -buildmode=c-shared -ldflags $ldflags `
        -o $outPath ./cmd/portway-engine
    if ($LASTEXITCODE -ne 0) { throw "go build 失败（退出码 $LASTEXITCODE）" }
} finally {
    Pop-Location
}

if (-not (Test-Path -LiteralPath $outPath)) { throw "构建结束但未生成 $outPath" }

$sizeMb = [math]::Round((Get-Item -LiteralPath $outPath).Length / 1MB, 1)
Write-Host "[engine] 产物     : $outPath ($sizeMb MB)"
Write-Host '[engine] 提示     : 客户端会在自身可执行文件同目录查找该库。'
