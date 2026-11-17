<#
Installs and manages Seelynx as a Windows service. Run it from PowerShell as Administrator.

  .\scripts\service.ps1 install -DataDir C:\ProgramData\Seelynx -MinFreeGB 50
  .\scripts\service.ps1 upgrade
  .\scripts\service.ps1 status
  .\scripts\service.ps1 uninstall

install and upgrade copy bin\seelynx.exe, MediaMTX and ffmpeg into Program Files. Your data folder
stays where it is, and uninstall never deletes it.
#>
param(
    [Parameter(Mandatory, Position = 0)][ValidateSet('install', 'upgrade', 'status', 'uninstall')][string]$Action,
    [string]$DataDir = (Join-Path $env:ProgramData 'Seelynx'),
    [string]$RecordingsDir = '',
    [int]$MinFreeGB = 50,
    [string]$Listen = '127.0.0.1:8480'
)
$ErrorActionPreference = 'Stop'

$Name = 'Seelynx'
$Account = "NT SERVICE\$Name"
$InstallDir = Join-Path $env:ProgramFiles $Name
$Repo = Split-Path $PSScriptRoot -Parent
$Sources = @{
    'seelynx.exe'  = Join-Path $Repo 'bin\seelynx.exe'
    'mediamtx.exe' = Join-Path $Repo '.tools\mediamtx\mediamtx.exe'
    'ffmpeg.exe'   = Join-Path $Repo '.tools\ffmpeg\ffmpeg.exe'
}

function Assert-Admin {
    $me = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
    if (-not $me.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this from PowerShell opened with Run as administrator.'
    }
}

function Get-Installed { Get-Service -Name $Name -ErrorAction SilentlyContinue }

function Copy-Programs {
    foreach ($file in $Sources.Keys) {
        if (-not (Test-Path $Sources[$file])) { throw "Can't find $($Sources[$file]). Build Seelynx first." }
    }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    foreach ($file in $Sources.Keys) { Copy-Item $Sources[$file] (Join-Path $InstallDir $file) -Force }
}

function Invoke-Checked {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "$Exe $($Arguments -join ' ') failed with code $LASTEXITCODE." }
}

function Wait-Ready {
    $port = ($Script:Url -split ':')[-1]
    for ($i = 0; $i -lt 30; $i++) {
        $service = Get-Installed
        if ($service.Status -eq 'Stopped') { break }
        if ((Test-NetConnection 127.0.0.1 -Port $port -WarningAction SilentlyContinue -InformationLevel Quiet)) {
            Write-Host "Seelynx is running: http://127.0.0.1:$port"
            return
        }
        Start-Sleep -Seconds 1
    }
    Show-Errors
    throw 'Seelynx did not start. See the errors above.'
}

function Show-Errors {
    Get-WinEvent -FilterHashtable @{ LogName = 'Application'; ProviderName = $Name } -MaxEvents 5 -ErrorAction SilentlyContinue |
        ForEach-Object { Write-Host "$($_.TimeCreated)  $($_.Message)" }
}

$Script:Url = $Listen

switch ($Action) {
    'install' {
        Assert-Admin
        if (Get-Installed) { throw "Seelynx is already installed. Use upgrade, or uninstall first." }

        # A copy started by hand holds the ports and the database.
        Get-Process -Name seelynx -ErrorAction SilentlyContinue | ForEach-Object {
            Write-Host "Stopping the running copy of Seelynx (process $($_.Id))."
            Stop-Process -Id $_.Id -Force
            $_.WaitForExit(10000) | Out-Null
        }

        $DataDir = [IO.Path]::GetFullPath($DataDir)
        New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
        Copy-Programs

        $exe = Join-Path $InstallDir 'seelynx.exe'
        $engine = Join-Path $InstallDir 'mediamtx.exe'
        $command = "`"$exe`" -data `"$DataDir`" -mediamtx `"$engine`" -listen $Listen -min-free-gb $MinFreeGB"
        if ($RecordingsDir) {
            $RecordingsDir = [IO.Path]::GetFullPath($RecordingsDir)
            New-Item -ItemType Directory -Force -Path $RecordingsDir | Out-Null
            $command += " -recordings `"$RecordingsDir`""
        }
        New-Service -Name $Name -DisplayName 'Seelynx VMS' -BinaryPathName $command -StartupType Automatic `
            -Description 'Records and serves video from your cameras.' | Out-Null

        # Its own limited account, instead of the all-powerful LocalSystem.
        Invoke-Checked sc.exe @('config', $Name, 'obj=', $Account)
        Invoke-Checked icacls.exe @($DataDir, '/grant', "${Account}:(OI)(CI)M", '/Q')
        if ($RecordingsDir) { Invoke-Checked icacls.exe @($RecordingsDir, '/grant', "${Account}:(OI)(CI)M", '/Q') }
        Invoke-Checked icacls.exe @($InstallDir, '/grant', "${Account}:(OI)(CI)RX", '/Q')

        # Restart after a crash: at once, then after 30 seconds, then each minute.
        Invoke-Checked sc.exe @('failure', $Name, 'reset=', '86400', 'actions=', 'restart/5000/restart/30000/restart/60000')
        Invoke-Checked sc.exe @('failureflag', $Name, '1')

        try { New-EventLog -LogName Application -Source $Name -ErrorAction Stop } catch { }

        Start-Service -Name $Name
        Wait-Ready
    }
    'upgrade' {
        Assert-Admin
        if (-not (Get-Installed)) { throw 'Seelynx is not installed. Use install first.' }
        Stop-Service -Name $Name
        # The data folder is in the service's own command line.
        $line = (Get-CimInstance Win32_Service -Filter "Name='$Name'").PathName
        if ($line -match '-data "([^"]+)"') {
            $backups = Join-Path $Matches[1] 'backups'
            $database = Join-Path $Matches[1] 'seelynx.db'
            if (Test-Path $database) {
                New-Item -ItemType Directory -Force -Path $backups | Out-Null
                $copy = Join-Path $backups ('seelynx-{0:yyyyMMdd-HHmmss}.db' -f (Get-Date))
                Copy-Item $database $copy
                Write-Host "Backed up the database to $copy"
            }
        }
        if ($line -match '-listen (\S+)') { $Script:Url = $Matches[1] }
        Copy-Programs
        Start-Service -Name $Name
        Wait-Ready
    }
    'status' {
        $service = Get-Installed
        if (-not $service) { Write-Host 'Seelynx is not installed as a service.'; break }
        $details = Get-CimInstance Win32_Service -Filter "Name='$Name'"
        Write-Host "State:    $($service.Status)"
        Write-Host "Startup:  $($details.StartMode)"
        Write-Host "Account:  $($details.StartName)"
        Write-Host "Command:  $($details.PathName)"
        Show-Errors
    }
    'uninstall' {
        Assert-Admin
        if (Get-Installed) {
            Stop-Service -Name $Name -ErrorAction SilentlyContinue
            Invoke-Checked sc.exe @('delete', $Name)
        }
        Remove-Item $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host 'Seelynx is uninstalled. Your data folder and recordings were not touched.'
    }
}
