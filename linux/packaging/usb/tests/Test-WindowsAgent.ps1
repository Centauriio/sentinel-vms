$ErrorActionPreference='Stop'
. "$PSScriptRoot\..\WindowsAgent.Common.ps1"
$listing=@'
[dshow] "Camera A" (video)
[dshow] Alternative name "@device_pnp_\\?\usb#vid_abcd&pid_1234\global"
[dshow] "Mic" (audio)
[dshow] Alternative name "audio-device"
'@
$devices=@(Read-DirectShowDevices $listing)
if ($devices.Count -ne 1 -or $devices[0].id -notmatch '^windows:[a-f0-9]{32}$') { throw 'Device parsing failed.' }
if ((Get-CameraIdentity $devices[0].captureName) -ne $devices[0].id) { throw 'Device identity is unstable.' }
$caps=@'
[dshow] vcodec=mjpeg min s=1920x1080 fps=30 max s=1920x1080 fps=30
[dshow] vcodec=mjpeg min s=1920x1080 fps=30 max s=1920x1080 fps=30 (pc)
[dshow] pixel_format=yuyv422 min s=640x480 fps=20 max s=640x480 fps=20
[dshow] vcodec=mjpeg min s=640x480 fps=5 max s=1920x1080 fps=60
[dshow] vcodec=mjpeg min s=8000x6000 fps=5 max s=8000x6000 fps=5
'@
$modes=@(Read-DirectShowModes $caps)
if ($modes.Count -ne 2 -or $modes[0].fps -ne 30) { throw 'Invented or duplicated capture mode.' }
$camera=[pscustomobject]@{ instance=('web-'+('a'*24)); url=('rtsp://publisher:'+('b'*48)+'@127.0.0.1:18555/camera'); profile=[pscustomobject]@{ format='mjpeg'; width=1920; height=1080; fps=30; bitrate=4000 } }
$argsList=@(Get-AgentCaptureArguments $camera $devices[0].captureName)
if ($argsList -notcontains "video=$($devices[0].captureName)" -or $argsList[-1] -ne $camera.url) { throw 'Capture arguments changed literal device or URL.' }
foreach ($value in @('', 'plain', 'has space', 'a"b', 'trailing\', 'C:\a b\')) {
    # Round-trip through a child process; validates Windows quoting rather than mirroring the algorithm.
    # PowerShell -EncodedCommand does not accept positional args, so use a temporary .ps1.
    $probe=Join-Path $env:TEMP ('sentinel-quote-'+[Guid]::NewGuid().ToString('N')+'.ps1')
    try {
        '[Console]::Write($args[0])' | Set-Content -LiteralPath $probe
        $actual=Invoke-AgentProbe "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" @('-NoProfile','-ExecutionPolicy','Bypass','-File',$probe,$value)
        if ($actual -cne $value) { throw 'Native argument round-trip failed.' }
    } finally { Remove-Item -LiteralPath $probe -ErrorAction SilentlyContinue }
}
# Force-closing the job must terminate the assigned child, without touching any other process.
$job=Initialize-CaptureJob
$child=New-AgentProcess "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" @('-NoProfile','-Command','Start-Sleep -Seconds 60') -DiscardOutput
try {
    $job.Add($child.Handle); $job.Dispose()
    if (-not $child.WaitForExit(5000)) { throw 'Capture survived closing its ownership job.' }
} finally { if (-not $child.HasExited) { $child.Kill() }; $child.Dispose(); $job.Dispose() }
Write-Host 'Windows camera discovery, discrete modes, literal arguments, and owned-process cleanup passed.'
