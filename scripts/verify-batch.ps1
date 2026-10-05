[CmdletBinding()]
param(
    [switch]$Race,
    [switch]$Benchmarks
)

$ErrorActionPreference = 'Stop'
$repositoryPath = Split-Path -Parent $PSScriptRoot
$originalPath = Get-Location
$originalCgo = $env:CGO_ENABLED

try {
    if ($Race) { $env:CGO_ENABLED = '1' }
    foreach ($moduleDirectory in @('.', 'core', 'extension')) {
        Set-Location -LiteralPath (Join-Path $repositoryPath $moduleDirectory)
        Write-Output "Verifying module: $moduleDirectory"
        $packageNames = @(& go list ./... | Where-Object {
            $_ -notmatch '^github\.com/iyear/tdl/test(?:/|$)'
        })
        if ($LASTEXITCODE -ne 0) { throw "Package discovery failed in $moduleDirectory" }
        & go build @packageNames
        if ($LASTEXITCODE -ne 0) { throw "Build failed in $moduleDirectory" }
        & go test @packageNames -count=1 -timeout=180s
        if ($LASTEXITCODE -ne 0) { throw "Tests failed in $moduleDirectory" }
        & go vet @packageNames
        if ($LASTEXITCODE -ne 0) { throw "Vet failed in $moduleDirectory" }
        if ($Race) {
            & go test -race @packageNames -count=1 -timeout=300s
            if ($LASTEXITCODE -ne 0) { throw "Race tests failed in $moduleDirectory" }
        }
    }
    if ($Benchmarks) {
        Set-Location -LiteralPath $repositoryPath
        & go test ./pkg/autodl -run '^$' -bench '^BenchmarkBatch' -benchmem -benchtime=1x -count=1
        if ($LASTEXITCODE -ne 0) { throw 'Batch benchmarks failed' }
        Set-Location -LiteralPath (Join-Path $repositoryPath 'core')
        & go test ./downloader -run '^$' -bench . -benchmem -benchtime=1x -count=1
        if ($LASTEXITCODE -ne 0) { throw 'Downloader benchmarks failed' }
    }
} finally {
    Set-Location -LiteralPath $originalPath
    $env:CGO_ENABLED = $originalCgo
}
