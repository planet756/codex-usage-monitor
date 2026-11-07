param(
    [Parameter(Mandatory)][string]$Installer,
    [Parameter(Mandatory)][string]$BuildDir,
    [Parameter(Mandatory)][string]$PreviousInstaller,
    [Parameter(Mandatory)][string]$PreviousBuildDir,
    [Parameter(Mandatory)][string]$Version,
    [ValidateSet('CurrentUser', 'AllUsers')][string]$Scope = 'CurrentUser'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'Installer smoke tests require Windows.'
}
$installerPath = (Resolve-Path -LiteralPath $Installer).Path
$payload = Join-Path (Resolve-Path -LiteralPath $BuildDir).Path 'CodexUsageMonitor.exe'
$previousInstallerPath = (Resolve-Path -LiteralPath $PreviousInstaller).Path
$previousPayload = Join-Path (Resolve-Path -LiteralPath $PreviousBuildDir).Path 'CodexUsageMonitor.exe'
$previousVersion = (& $previousPayload --version | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $previousVersion -ceq "Codex Usage Monitor $Version") {
    throw 'The upgrade fixture must have a different application version.'
}
$userUninstallKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexUsageMonitor'
$machineUninstallKey = 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexUsageMonitor'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$personalShortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'Codex Usage Monitor.lnk'
$commonShortcut = Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'Codex Usage Monitor.lnk'
$userInstallDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Programs\CodexUsageMonitor'
$machineInstallDir = Join-Path $env:ProgramW6432 'CodexUsageMonitor'
if ($Scope -eq 'AllUsers') {
    $principal = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
    if (!$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'All-users smoke tests require an elevated Windows test account.'
    }
    $uninstallKey = $machineUninstallKey
    $otherUninstallKey = $userUninstallKey
    $shortcut = $commonShortcut
    $defaultInstallDir = $machineInstallDir
} else {
    $uninstallKey = $userUninstallKey
    $otherUninstallKey = $machineUninstallKey
    $shortcut = $personalShortcut
    $defaultInstallDir = $userInstallDir
}
$existingStartup = Get-ItemProperty -LiteralPath $runKey -Name CodexUsageMonitor -ErrorAction SilentlyContinue
if ((Test-Path $userUninstallKey) -or (Test-Path $machineUninstallKey) -or
    (Test-Path -LiteralPath $personalShortcut) -or (Test-Path -LiteralPath $commonShortcut) -or
    (Test-Path -LiteralPath $userInstallDir) -or (Test-Path -LiteralPath $machineInstallDir) -or $existingStartup -or
    (Get-Process CodexUsageMonitor -ErrorAction SilentlyContinue)) {
    throw 'Run the installer smoke test on a clean Windows test account.'
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('cum-installer-' + [guid]::NewGuid().ToString('N'))
$installDir = $defaultInstallDir
$download = Join-Path $testRoot 'Downloaded Setup.exe'
$oldLocalAppData = $env:LOCALAPPDATA
$testProfile = Join-Path $testRoot 'Profile'
$config = Join-Path $testProfile 'CodexUsageMonitor\settings.json'
$installed = Join-Path $installDir 'CodexUsageMonitor.exe'
$uninstaller = Join-Path $installDir 'Uninstall.exe'
$watcher = $null

function Wait-Until([scriptblock]$Condition, [string]$Failure) {
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    throw $Failure
}

function Invoke-Setup([string]$SetupPath, [switch]$DefaultDirectory) {
    Copy-Item -LiteralPath $SetupPath -Destination $download
    # NSIS requires /D last, without quotes even when the path contains spaces.
    $arguments = if ($DefaultDirectory) { "/S /$Scope" } else { "/S /$Scope /D=$installDir" }
    $process = Start-Process -FilePath $download -ArgumentList $arguments -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "Setup failed with exit code $($process.ExitCode)" }
    Remove-Item -LiteralPath $download
}

function Invoke-Uninstall {
    $process = Start-Process -FilePath $uninstaller -ArgumentList "/S /$Scope" -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw 'Uninstallation failed' }
    Wait-Until { !(Test-Path -LiteralPath $installed) -and !(Test-Path -LiteralPath $uninstaller) } 'Installed binaries remain after uninstall'
}

function Assert-Version([string]$ExpectedPayload, [string]$ExpectedVersion) {
    $actual = (& $installed --version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $actual -cne $ExpectedVersion) { throw 'Installed version mismatch' }
    if ((Get-FileHash $installed).Hash -cne (Get-FileHash $ExpectedPayload).Hash) { throw 'Installed payload mismatch' }
}

function Start-Watcher {
    $process = Start-Process -FilePath $installed -ArgumentList '--watcher' -PassThru
    try {
        Wait-Until {
            if ($process.HasExited) { throw 'Installed watcher exited during startup' }
            try {
                $mutex = [Threading.Mutex]::OpenExisting('Local\CodexUsageMonitor.Watcher')
                $mutex.Dispose()
                return $true
            } catch [Threading.WaitHandleCannotBeOpenedException] { return $false }
        } 'Installed watcher did not finish startup'
    } catch {
        if (!$process.HasExited) { $process.Kill() }
        throw
    }
    return $process
}

try {
    Write-Host "Installer smoke checks: $Scope"
    New-Item -ItemType Directory -Force -Path $testProfile | Out-Null
    # Exercise the default for a first install, without /D or a precreated target.
    Invoke-Setup $installerPath -DefaultDirectory
    Assert-Version $payload "Codex Usage Monitor $Version"
    $registration = Get-ItemProperty -LiteralPath $uninstallKey
    if ($registration.InstallLocation -ine $defaultInstallDir) { throw 'Default installation directory mismatch' }
    if ($registration.UninstallString -notlike "* /$Scope" -or (Test-Path $otherUninstallKey)) {
        throw 'Installation scope was not preserved in uninstall registration'
    }
    Invoke-Uninstall
    if ((Test-Path -LiteralPath $defaultInstallDir) -or (Test-Path $uninstallKey) -or
        (Test-Path -LiteralPath $shortcut)) {
        throw 'Default-directory installation did not cleanly uninstall'
    }

    $installDir = Join-Path $testRoot 'Selected Folder\Installed App'
    $installed = Join-Path $installDir 'CodexUsageMonitor.exe'
    $uninstaller = Join-Path $installDir 'Uninstall.exe'
    $env:LOCALAPPDATA = $testProfile
    Invoke-Setup $previousInstallerPath
    Assert-Version $previousPayload $previousVersion
    if (!(Test-Path -LiteralPath $shortcut) -or !(Test-Path $uninstallKey)) { throw 'Installation registration missing' }
    $link = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut)
    if ($link.TargetPath -ine $installed) { throw 'Shortcut still points at the downloaded installer' }
    $link = $null

    # The downloaded setup has already been removed before launching the app.
    $watcher = Start-Watcher
    Wait-Until { Test-Path -LiteralPath $config } 'Installed application did not create settings'
    Wait-Until {
        $entry = Get-ItemProperty -LiteralPath $runKey -Name CodexUsageMonitor -ErrorAction SilentlyContinue
        $entry -and $entry.CodexUsageMonitor.StartsWith('"' + $installed + '" ', [StringComparison]::OrdinalIgnoreCase)
    } 'Startup does not point to the installed application'

    # A GUI-subsystem EXE runs asynchronously at the end of a PowerShell pipeline.
    # Pipe one-shot commands so they finish and update LASTEXITCODE before assertions.
    & $installed --add-start-menu --config $config | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Manual shortcut registration failed' }
    if ($Scope -eq 'AllUsers' -and (Test-Path -LiteralPath $personalShortcut)) {
        throw 'All-users installation created a personal shortcut shadowing the common entry'
    }

    $settings = Get-Content -Raw -LiteralPath $config | ConvertFrom-Json
    $settings.language = 'zh-CN'
    $settings | ConvertTo-Json | Set-Content -LiteralPath $config -Encoding utf8NoBOM
    $savedSettings = (Get-FileHash $config).Hash
    Invoke-Setup $installerPath
    if (!$watcher.WaitForExit(15000)) { throw 'Setup did not stop the old watcher' }
    $watcher = $null
    Assert-Version $payload "Codex Usage Monitor $Version"
    if ((Get-FileHash $config).Hash -cne $savedSettings) { throw 'Upgrade replaced user settings' }

    & $installed --uninstall-startup | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not set startup opt-out' }
    $settings = Get-Content -Raw -LiteralPath $config | ConvertFrom-Json
    if ($settings.start_with_windows -ne $false -or
        (Get-ItemProperty -LiteralPath $runKey -Name CodexUsageMonitor -ErrorAction SilentlyContinue)) {
        throw "Startup opt-out was not established before reinstallation ($Scope)"
    }
    $savedSettings = (Get-FileHash $config).Hash
    Invoke-Setup $installerPath
    $watcher = Start-Watcher
    Wait-Until { Test-Path -LiteralPath $config } 'Settings missing after reinstallation'
    if ($watcher.HasExited) {
        throw "Watcher exited after reinstallation ($Scope), exit code $($watcher.ExitCode)"
    }
    if ((Get-FileHash $config).Hash -cne $savedSettings) {
        throw "Reinstallation changed saved settings ($Scope)"
    }
    if (Get-ItemProperty -LiteralPath $runKey -Name CodexUsageMonitor -ErrorAction SilentlyContinue) {
        throw "Reinstallation restored startup registration after opt-out ($Scope)"
    }

    & $installed --install-startup | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restore startup for uninstall test' }
    $savedSettings = (Get-FileHash $config).Hash
    $userFile = Join-Path $installDir 'user-note.txt'
    Set-Content -LiteralPath $userFile -Value 'Preserve files not owned by the installer.'
    Invoke-Uninstall
    if (!$watcher.WaitForExit(15000)) { throw 'Uninstall left the watcher running' }
    $watcher = $null
    if ((Test-Path -LiteralPath $shortcut) -or (Test-Path $uninstallKey) -or
        (Get-ItemProperty -LiteralPath $runKey -Name CodexUsageMonitor -ErrorAction SilentlyContinue)) {
        throw 'Uninstall left application registrations behind'
    }
    if (!(Test-Path -LiteralPath $userFile) -or (Get-FileHash $config).Hash -cne $savedSettings) {
        throw 'Uninstall removed user data'
    }
    Write-Host "Installer smoke checks passed for $Scope."
}
finally {
    if (Test-Path -LiteralPath $uninstaller) {
        try {
            Invoke-Uninstall
        } catch { Write-Warning "Test cleanup failed: $_" }
    }
    if ($watcher -and !$watcher.HasExited) {
        if (Test-Path -LiteralPath $installed) { & $installed --stop | Out-Null }
        if (!$watcher.WaitForExit(15000)) { $watcher.Kill() }
    }
    $env:LOCALAPPDATA = $oldLocalAppData
}
