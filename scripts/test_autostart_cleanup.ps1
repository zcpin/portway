param([string]$CompilerPath)

$ErrorActionPreference = 'Stop'
if (-not $CompilerPath) {
    $compilerCommand = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($compilerCommand) { $CompilerPath = $compilerCommand.Source }
}
if (-not $CompilerPath) {
    $candidates = @(
        (Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 6\ISCC.exe'),
        (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'),
        (Join-Path $env:ProgramFiles 'Inno Setup 6\ISCC.exe')
    )
    $CompilerPath = $candidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
}
if (-not $CompilerPath) { throw 'Inno Setup 6 compiler was not found.' }

$testDirectory = Join-Path ([IO.Path]::GetTempPath()) ('ssh-tunnel-autostart-tests-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($testDirectory) | Out-Null
$utf8 = [Text.UTF8Encoding]::new($false)

# Compile the real installer using harmless payload fixtures, without running it.
$bundleDirectory = Join-Path $testDirectory 'bundle'
[IO.Directory]::CreateDirectory($bundleDirectory) | Out-Null
foreach ($name in @('portway.exe', 'portway-daemon.exe')) {
    [IO.File]::WriteAllText((Join-Path $bundleDirectory $name), 'fixture', $utf8)
}
& $CompilerPath '/Qp' "/O$testDirectory" "/DAppBundleDir=$bundleDirectory" (Join-Path $PSScriptRoot 'installer.iss')
if ($LASTEXITCODE -ne 0) { throw 'Installer compilation failed.' }

& $CompilerPath '/Qp' "/O$testDirectory" (Join-Path $PSScriptRoot 'test_autostart_cleanup.iss')
if ($LASTEXITCODE -ne 0) { throw 'Autostart matching test compilation failed.' }

$resultFile = Join-Path $testDirectory 'result.txt'
$arguments = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/SP-', '/NORESTART', ('/ResultFile="' + $resultFile + '"'))
$process = Start-Process -FilePath (Join-Path $testDirectory 'autostart-cleanup-tests.exe') -ArgumentList $arguments -WindowStyle Hidden -Wait -PassThru
if (-not (Test-Path -LiteralPath $resultFile)) { throw "Matching tests did not produce a result (exit $($process.ExitCode))." }
$result = Get-Content -LiteralPath $resultFile -Raw -Encoding UTF8
if ($result -ne 'PASS 14') { throw "Autostart matching tests failed: $result" }
Write-Output 'Installer compiled; 14 autostart path matching cases passed.'
