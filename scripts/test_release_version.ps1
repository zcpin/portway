$ErrorActionPreference = 'Stop'

$resolver = Join-Path $PSScriptRoot 'resolve_release_version.ps1'
$testDirectory = Join-Path ([IO.Path]::GetTempPath()) ('portway-version-tests-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($testDirectory) | Out-Null
$utf8 = [Text.UTF8Encoding]::new($false)
$pubspecPath = Join-Path $testDirectory 'pubspec.yaml'
[IO.File]::WriteAllText($pubspecPath, "name: version_fixture`nversion: 0.8.4-dev.1+9`n", $utf8)

$cases = @(
    @{ Type = 'tag'; Name = 'v1.2.3'; Version = '1.2.3'; BuildName = '1.2.3'; Prerelease = 'false' },
    @{ Type = 'tag'; Name = 'v1.2.3+build-rc.1'; Version = '1.2.3+build-rc.1'; BuildName = '1.2.3'; Prerelease = 'false' },
    @{ Type = 'tag'; Name = 'v1.2.3-beta'; Version = '1.2.3-beta'; BuildName = '1.2.3'; Prerelease = 'true' },
    @{ Type = 'tag'; Name = 'v2.0.0-rc.1+build.8'; Version = '2.0.0-rc.1+build.8'; BuildName = '2.0.0'; Prerelease = 'true' },
    @{ Type = 'branch'; Name = 'main'; Version = 'ci-42'; BuildName = '0.8.4'; Prerelease = 'false' },
    @{ Type = 'branch'; Name = 'feature/release'; Version = 'ci-42'; BuildName = '0.8.4'; Prerelease = 'false' },
    @{ Type = 'branch'; Name = 'v-next'; Version = 'ci-42'; BuildName = '0.8.4'; Prerelease = 'false' }
)

foreach ($case in $cases) {
    $outputPath = Join-Path $testDirectory ([guid]::NewGuid().ToString('N') + '.outputs')
    $actual = & $resolver -RefType $case.Type -RefName $case.Name -RunNumber '42' -PubspecPath $pubspecPath -OutputPath $outputPath | ConvertFrom-Json
    if ($actual.version -cne $case.Version -or $actual.build_name -cne $case.BuildName -or $actual.build_number -cne '42' -or $actual.prerelease -cne $case.Prerelease) {
        throw "Unexpected version metadata for $($case.Type) $($case.Name): $($actual | ConvertTo-Json -Compress)"
    }
    $expectedOutput = "version=$($case.Version)`nbuild_name=$($case.BuildName)`nbuild_number=42`nprerelease=$($case.Prerelease)`n"
    if ([IO.File]::ReadAllText($outputPath) -cne $expectedOutput) {
        throw "Incorrect GitHub Actions outputs for $($case.Name)"
    }
}

foreach ($tag in @('v', '1.2.3', 'v1.2', 'v1.02.3', 'v1.2.3-01', 'v1.2.3/path')) {
    $rejected = $false
    try {
        & $resolver -RefType tag -RefName $tag -RunNumber '42' -OutputPath '' | Out-Null
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw "Invalid tag was accepted: $tag"
    }
}

foreach ($run in @('', '0', '-1', '42/path')) {
    $rejected = $false
    try {
        & $resolver -RefType branch -RefName main -RunNumber $run -PubspecPath $pubspecPath -OutputPath '' | Out-Null
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw "Invalid run number was accepted: $run"
    }
}

Write-Output 'Release version tests passed (7 valid cases, 10 invalid cases).'
