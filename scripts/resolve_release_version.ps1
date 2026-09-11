param(
    [ValidateSet('tag', 'branch')]
    [string]$RefType = $env:GITHUB_REF_TYPE,
    [string]$RefName = $env:GITHUB_REF_NAME,
    [string]$RunNumber = $env:GITHUB_RUN_NUMBER,
    [string]$PubspecPath = (Join-Path $PSScriptRoot '../client/pubspec.yaml'),
    [string]$OutputPath = $env:GITHUB_OUTPUT
)

$ErrorActionPreference = 'Stop'

if ($RunNumber -notmatch '^[1-9][0-9]*$') {
    throw 'RunNumber must be a positive integer.'
}

$numericIdentifier = '(?:0|[1-9][0-9]*)'
$prereleaseIdentifier = '(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
$semver = "(?<build_name>$numericIdentifier\.$numericIdentifier\.$numericIdentifier)(?:-$prereleaseIdentifier(?:\.$prereleaseIdentifier)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"

if ($RefType -eq 'tag') {
    $versionMatch = [regex]::Match($RefName, "^v(?<version>$semver)$")
    if (-not $versionMatch.Success) {
        throw "Release tag must be v<semver>, for example v1.2.3 or v1.2.3-rc.1: $RefName"
    }
    $version = $versionMatch.Groups['version'].Value
} else {
    $pubspec = Get-Content -LiteralPath $PubspecPath -Raw -Encoding UTF8
    $versionMatch = [regex]::Match($pubspec, "(?m)^version:[\t ]*$semver[\t ]*\r?$")
    if (-not $versionMatch.Success) {
        throw "Cannot read a semantic version from $PubspecPath"
    }
    # 分支名可能包含斜杠；手动构建的产物名始终使用运行序号。
    $version = "ci-$RunNumber"
}

$metadata = [ordered]@{
    version = $version
    build_name = $versionMatch.Groups['build_name'].Value
    build_number = $RunNumber
}

if ($OutputPath) {
    $utf8 = [Text.UTF8Encoding]::new($false)
    foreach ($entry in $metadata.GetEnumerator()) {
        [IO.File]::AppendAllText($OutputPath, "$($entry.Key)=$($entry.Value)`n", $utf8)
    }
}

$metadata | ConvertTo-Json -Compress
