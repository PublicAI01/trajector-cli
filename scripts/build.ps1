# Windows equivalent of build.sh: stamp the source version, without marking
# the binary as an official release or enabling automatic updates.
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Version,
    [string]$Out = 'trajector.exe'
)

$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    if (-not $Version) {
        $Version = & git describe --tags --dirty 2>$null
        if ($LASTEXITCODE -ne 0 -or -not $Version) {
            throw 'No git tags to describe. Fetch tags or pass a release version: .\scripts\build.ps1 v0.6.0'
        }
    }

    & go build -trimpath -ldflags "-X github.com/PublicAI01/trajector-cli/internal/cli.version=$Version" -o $Out ./cmd/trajector
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed (exit $LASTEXITCODE)"
    }
} finally {
    Pop-Location
}
