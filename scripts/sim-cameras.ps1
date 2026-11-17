# Runs a fake RTSP camera on port 18554 for development and load testing.
#   rtsp://127.0.0.1:18554/main   1080p, about 4 Mbit/s
#   rtsp://127.0.0.1:18554/sub    360p, about 0.5 Mbit/s
# Any number of Seelynx cameras can point at these URLs, so a 100-camera load
# test needs just this one process. Stop it with Ctrl+C.

$ErrorActionPreference = 'Stop'
$root   = Split-Path -Parent $PSScriptRoot
$sim    = Join-Path $root '.tools\sim'
$ffmpeg = Join-Path $root '.tools\ffmpeg\ffmpeg.exe'
$mtx    = Join-Path $root '.tools\mediamtx\mediamtx.exe'
New-Item -ItemType Directory -Force $sim | Out-Null

function New-Clip($name, $size, $bitrate) {
    $out = Join-Path $sim "$name.mp4"
    if (Test-Path $out) { return }
    & $ffmpeg -hide_banner -loglevel error -f lavfi -i "testsrc2=size=${size}:rate=25" -t 60 `
        -c:v libx264 -preset veryfast -profile:v main -pix_fmt yuv420p -bf 0 -g 25 `
        -b:v $bitrate -maxrate $bitrate -bufsize $bitrate -an $out
    if ($LASTEXITCODE -ne 0) { throw "ffmpeg failed creating $name" }
}
New-Clip 'main' '1920x1080' '4M'
New-Clip 'sub'  '640x360'   '500k'

$ff = $ffmpeg -replace '\\', '/'
$dir = $sim -replace '\\', '/'
function Publish($name) { "$ff -hide_banner -loglevel error -re -stream_loop -1 -i $dir/$name.mp4 -c copy -f rtsp -rtsp_transport tcp rtsp://127.0.0.1:18554/$name" }

$config = @"
logLevel: warn
rtspTransports: [tcp]
rtspAddress: 127.0.0.1:18554
rtmp: false
hls: false
srt: false
moq: false
webrtc: false
paths:
  main:
    runOnInit: $(Publish 'main')
    runOnInitRestart: true
  sub:
    runOnInit: $(Publish 'sub')
    runOnInitRestart: true
"@
$cfg = Join-Path $sim 'mediamtx.yml'
Set-Content -Path $cfg -Value $config -Encoding ascii
& $mtx $cfg
