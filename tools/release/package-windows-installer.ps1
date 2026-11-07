param(
    [Parameter(Mandatory)][string]$BuildDir,
    [Parameter(Mandatory)][string]$OutputDir,
    [string]$Version = $env:BUILD_VERSION
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'This wrapper verifies the payload on Windows and requires NSIS 3.'
}
if (!$Version) { $Version = 'dev' }
if ($Version -notmatch '\A[0-9A-Za-z.+-]+\z') { throw 'Invalid installer version' }
$numericVersion = '0.0.0.0'
if ($Version -ne 'dev') {
    $core = ($Version -split '[-+]', 2)[0]
    $numbers = $core.Split('.')
    if ($numbers.Count -ne 3) { throw 'Expected a semantic version' }
    foreach ($number in $numbers) {
        if ($number -notmatch '\A(0|[1-9][0-9]*)\z' -or [long]$number -gt 65535) {
            throw 'Installer version components must be between 0 and 65535'
        }
    }
    $numericVersion = "$core.0"
}

$source = (Resolve-Path -LiteralPath $BuildDir).Path
$binary = Join-Path $source 'CodexUsageMonitor.exe'
$actualVersion = (& $binary --version | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $actualVersion -cne "Codex Usage Monitor $Version") {
    throw 'Installer payload version mismatch'
}
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$destination = (Resolve-Path -LiteralPath $OutputDir).Path
if ($destination.TrimEnd('\') -ieq $source.TrimEnd('\')) {
    throw 'Use a separate output directory so the portable ZIP contains only the application'
}
$compiler = Join-Path ${env:ProgramFiles(x86)} 'NSIS\makensis.exe'
if (!(Test-Path -LiteralPath $compiler)) {
    $compiler = (Get-Command makensis.exe -ErrorAction Stop).Source
}
$label = if ($Version -eq 'dev') { 'dev' } else { "v$Version" }
$name = "CodexUsageMonitor-$label-windows-amd64-setup"
$arguments = @('/V2', '/WX', '/NOCONFIG', "/DAppVersion=$Version", "/DNumericVersion=$numericVersion",
    "/DBuildDir=$source", "/DInstallerDir=$destination", "/DInstallerName=$name",
    (Join-Path $PSScriptRoot 'windows-installer.nsi'))
& $compiler @arguments | Write-Host
if ($LASTEXITCODE -ne 0) { throw 'Installer compilation failed' }
$installer = Join-Path $destination "$name.exe"
$hash = (Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText((Join-Path $destination 'SHA256SUMS'), "$hash  $name.exe`n", [Text.Encoding]::ASCII)
Write-Output $installer
