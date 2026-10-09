# CMake caches contain absolute paths and cannot be reused after moving a checkout.
# Preserve an incompatible build directory so Flutter can generate a fresh one.
$ErrorActionPreference = 'Stop'

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$buildDir = Join-Path $repoRoot 'client\build\windows\x64'
$sourceDir = Join-Path $repoRoot 'client\windows'
$cacheFile = Join-Path $buildDir 'CMakeCache.txt'

if (-not (Test-Path -LiteralPath $cacheFile -PathType Leaf)) { return }

$cachedPaths = @{}
foreach ($line in Get-Content -LiteralPath $cacheFile -Encoding UTF8) {
    if ($line -match '^(CMAKE_CACHEFILE_DIR|CMAKE_HOME_DIRECTORY):INTERNAL=(.*)$') {
        $cachedPaths[$Matches[1]] = $Matches[2]
    }
}

$expectedPaths = @{
    CMAKE_CACHEFILE_DIR = $buildDir
    CMAKE_HOME_DIRECTORY = $sourceDir
}
$stale = $false
foreach ($key in $expectedPaths.Keys) {
    $cachedPath = $cachedPaths[$key]
    $matchesPath = $false
    if ($cachedPath) {
        try {
            $normalized = [IO.Path]::GetFullPath($cachedPath).TrimEnd('\', '/')
            $matchesPath = [StringComparer]::OrdinalIgnoreCase.Equals($normalized, $expectedPaths[$key])
        } catch {
            # Invalid or incomplete cache paths also require a fresh build directory.
        }
    }
    if (-not $matchesPath) {
        $stale = $true
        Write-Host "  Stale CMake cache ${key}: $cachedPath"
        Write-Host "  Expected: $($expectedPaths[$key])"
    }
}
if (-not $stale) { return }

# Validate the rename stays inside this checkout and does not traverse a junction.
$item = Get-Item -LiteralPath $buildDir
$prefix = $repoRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
if (-not $item.FullName.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Build directory is outside the checkout: $($item.FullName)"
}
while ($null -ne $item) {
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "Refusing to rename a build directory through a junction or symlink: $($item.FullName)"
    }
    if ([StringComparer]::OrdinalIgnoreCase.Equals($item.FullName, $repoRoot)) { break }
    $item = $item.Parent
}

$backupName = 'x64.previous-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$backupDir = Join-Path (Split-Path -Parent $buildDir) $backupName
Write-Host "  Preserving previous Windows build: $buildDir"
Write-Host "  Backup: $backupDir"
Rename-Item -LiteralPath $buildDir -NewName $backupName
