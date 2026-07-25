[CmdletBinding()]
param(
    [Parameter()][string]$Rin = ".\rin.exe",
    [Parameter()][string]$DataDirectory = ".\rin-data",
    [Parameter()][string]$Address = "127.0.0.1:7374"
)

$ErrorActionPreference = "Stop"
if (-not (Test-Path -LiteralPath $Rin -PathType Leaf)) {
    throw "Rin executable not found: $Rin"
}
$data = [System.IO.Path]::GetFullPath($DataDirectory)
New-Item -ItemType Directory -Force -Path $data | Out-Null
Write-Host "Starting Rin at $Address with data $data"
& $Rin serve --addr $Address --data $data
exit $LASTEXITCODE
