# 编译并运行旧版本卸载逻辑的用例。
#
# 与 test_autostart_cleanup.ps1 同一套路：用 Inno Setup 编译器把 .iss 里的
# 纯字符串/路径判定编译成一个小程序，跑完把结果写进文件再断言。
#
# 该测试不执行产品安装或卸载，也不读写注册表。
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

$testDirectory = Join-Path ([IO.Path]::GetTempPath()) ('portway-uninstall-tests-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($testDirectory) | Out-Null

try {
    & $CompilerPath '/Qp' "/O$testDirectory" (Join-Path $PSScriptRoot 'test_legacy_install.iss')
    if ($LASTEXITCODE -ne 0) { throw 'Uninstall test compilation failed.' }

    $resultFile = Join-Path $testDirectory 'result.txt'
    $arguments = @(
        '/VERYSILENT', '/SUPPRESSMSGBOXES', '/SP-', '/NORESTART',
        ('/ResultFile="' + $resultFile + '"')
    )
    $process = Start-Process -FilePath (Join-Path $testDirectory 'uninstall-tests.exe') `
        -ArgumentList $arguments -WindowStyle Hidden -Wait -PassThru
    if (-not (Test-Path -LiteralPath $resultFile)) {
        throw "Uninstall tests did not produce a result (exit $($process.ExitCode))."
    }
    $result = Get-Content -LiteralPath $resultFile -Raw -Encoding UTF8
    if ($result -notmatch '^PASS (\d+)$') { throw "Uninstall tests failed: $result" }
    Write-Output "Previous version uninstall: $($Matches[1]) cases passed."
}
finally {
    Remove-Item -LiteralPath $testDirectory -Recurse -Force -ErrorAction SilentlyContinue
}
