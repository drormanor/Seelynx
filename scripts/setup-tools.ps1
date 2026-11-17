# Downloads the development tools into .tools\ inside this project.
# Nothing is installed system-wide: no PATH, registry, or service changes.
# Delete the .tools folder to remove everything.
#
# Go is checked against a pinned SHA-256. MediaMTX and ffmpeg are checked only if
# the publisher's checksum file can be downloaded; otherwise the script says so.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$root  = Split-Path -Parent $PSScriptRoot
$tools = Join-Path $root '.tools'
$dl    = Join-Path $tools 'dl'
New-Item -ItemType Directory -Force $dl | Out-Null

function Get-Sha256($path) { (Get-FileHash $path -Algorithm SHA256).Hash.ToLower() }

function Test-PublishedChecksum($zipPath, $checksumUrl, $assetName) {
    try {
        $text = (Invoke-WebRequest $checksumUrl -UseBasicParsing).Content
        if ($text -is [byte[]]) { $text = [Text.Encoding]::UTF8.GetString($text) }
    } catch {
        Write-Warning "$assetName : no checksum file found at $checksumUrl, NOT verified"
        return
    }
    $line = ($text -split "`n") | Where-Object { $_ -match [regex]::Escape($assetName) } | Select-Object -First 1
    if (-not $line) { Write-Warning "$assetName : not listed in checksum file, NOT verified"; return }
    $expected = ($line.Trim() -split '\s+')[0].ToLower()
    if ((Get-Sha256 $zipPath) -ne $expected) { throw "$assetName : checksum mismatch" }
    Write-Output "$assetName : checksum OK"
}

# Go
$goZip = Join-Path $dl 'go1.27.3.windows-amd64.zip'
Invoke-WebRequest 'https://go.dev/dl/go1.27.3.windows-amd64.zip' -OutFile $goZip -UseBasicParsing
if ((Get-Sha256 $goZip) -ne 'f26081f438319757f8a1b46d7d0b2c5ec7583d4038e485e0aa725cfde7885fff') { throw 'Go: checksum mismatch' }
Write-Output 'Go: checksum OK'
Expand-Archive $goZip -DestinationPath $tools -Force

# MediaMTX
$mtxVersion = 'v1.21.3'
$mtxName = "mediamtx_${mtxVersion}_windows_amd64.zip"
$mtxBase = "https://github.com/bluenviron/mediamtx/releases/download/$mtxVersion"
$mtxZip  = Join-Path $dl $mtxName
Invoke-WebRequest "$mtxBase/$mtxName" -OutFile $mtxZip -UseBasicParsing
Test-PublishedChecksum $mtxZip "$mtxBase/checksums.sha256" $mtxName
Expand-Archive $mtxZip -DestinationPath (Join-Path $tools 'mediamtx') -Force

# ffmpeg: reads snapshots from recordings, and simulates cameras during development
$ffName = 'ffmpeg-master-latest-win64-gpl.zip'
$ffBase = 'https://github.com/BtbN/FFmpeg-Builds/releases/download/latest'
$ffZip  = Join-Path $dl $ffName
Invoke-WebRequest "$ffBase/$ffName" -OutFile $ffZip -UseBasicParsing
Test-PublishedChecksum $ffZip "$ffBase/checksums.sha256" $ffName
$ffTmp = Join-Path $dl 'ffmpeg-extracted'
Expand-Archive $ffZip -DestinationPath $ffTmp -Force
$ffDir = Join-Path $tools 'ffmpeg'
New-Item -ItemType Directory -Force $ffDir | Out-Null
Get-ChildItem $ffTmp -Recurse -Include 'ffmpeg.exe','ffprobe.exe' | Copy-Item -Destination $ffDir -Force
Remove-Item $ffTmp -Recurse -Force

Write-Output "Done. Tools are in $tools"
