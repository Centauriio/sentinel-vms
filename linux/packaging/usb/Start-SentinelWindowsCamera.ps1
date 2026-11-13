[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^[a-z][a-z0-9-]{0,31}$')][string]$Name,
    [Parameter(Mandatory)][string]$CameraName,
    [string]$Distribution = 'Ubuntu'
)
. "$PSScriptRoot\WslUsb.Common.ps1"
$ffmpeg = Resolve-CaptureFFmpeg
if (-not $ffmpeg) { throw 'Run Install-SentinelWslUsb.ps1 -Backend WindowsCapture first.' }
Assert-CaptureFFmpeg $ffmpeg
$connectionLines = & wsl.exe -d $Distribution -- sudo -- sentinel-usb connection $Name
if ($LASTEXITCODE -ne 0) { throw 'Could not load this camera. Configure and start its windows-publisher instance in WSL first.' }
$connection = ($connectionLines -join "`n") | ConvertFrom-Json
$target = [Uri]$connection.url
$publishKey = $target.UserInfo.Split(':', 2)[1]
if ($target.Scheme -ne 'rtsp' -or $publishKey -notmatch '^[0-9a-f]{48}$') { throw 'Invalid publishing connection.' }
$fps = [Convert]::ToString($connection.fps, [Globalization.CultureInfo]::InvariantCulture)
$arguments = @('-hide_banner', '-loglevel', 'warning', '-stats', '-stats_period', '5',
    '-f', 'dshow', '-rtbufsize', '128M', '-video_size', "$($connection.width)x$($connection.height)", '-framerate', $fps)
if ($connection.format -eq 'mjpeg') { $arguments += @('-vcodec', 'mjpeg') }
else { $arguments += @('-pixel_format', $connection.format) }
$arguments += @('-i', "video=$CameraName", '-an')
if ($connection.format -eq 'mjpeg') {
    $arguments += @('-vf', 'scale=in_range=full:out_range=limited', '-color_range', 'tv')
}
$arguments += @('-c:v', 'libx264', '-preset', 'veryfast', '-tune', 'zerolatency', '-pix_fmt', 'yuv420p',
    '-b:v', "$($connection.bitrate)k", '-maxrate', "$($connection.bitrate)k", '-bufsize', "$($connection.bitrate * 2)k",
    '-g', [string][Math]::Round($connection.fps * 2), '-sc_threshold', '0', '-fps_mode', 'passthrough',
    '-f', 'rtsp', '-rtsp_transport', 'tcp', $connection.url)
$savedErrorPreference = $ErrorActionPreference
try {
    $ErrorActionPreference = 'Continue'
    $PSNativeCommandUseErrorActionPreference = $false
    Write-Host "Publishing camera '$CameraName'. Keep this window open; Ctrl+C stops capture."
    & $ffmpeg @arguments 2>&1 | ForEach-Object { $_.ToString().Replace($publishKey, '[REDACTED]') }
    if ($LASTEXITCODE -ne 0) { throw "Camera publisher stopped (exit $LASTEXITCODE). Review the redacted output." }
} finally {
    $ErrorActionPreference = $savedErrorPreference
    $connectionLines = $null
    $connection = $null
    $arguments = $null
    $publishKey = $null
    $target = $null
}
