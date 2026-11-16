# Diagnose the installed Windows helper without starting capture or changing services.
[CmdletBinding()]
param([string]$Distribution = 'Ubuntu')
$ErrorActionPreference = 'Stop'
$phase = 'Load installed helper'
try {
    $common = Join-Path $env:LOCALAPPDATA 'Sentinel\UsbCamera\WindowsAgent.Common.ps1'
    if (-not (Test-Path -LiteralPath $common)) { throw 'The helper files are missing for this Windows account.' }
    . $common
    Write-Host ('PowerShell: ' + $PSVersionTable.PSVersion)
    $tasks = @(Get-ScheduledTask -TaskName 'Sentinel USB Camera*' -ErrorAction SilentlyContinue)
    foreach ($task in $tasks) {
        $info = $task | Get-ScheduledTaskInfo
        Write-Host ('Task: {0}; State: {1}; Last result: {2}' -f $task.TaskName, $task.State, $info.LastTaskResult)
    }
    $phase = 'Find FFmpeg'
    $ffmpeg = (Get-Command ffmpeg.exe -ErrorAction Stop).Source
    $phase = 'Read WSL identity'
    $identity = Invoke-AgentProbe 'wsl.exe' @('-d', $Distribution, '--exec', 'id') -TimeoutMs 15000
    Write-Host ('WSL identity: ' + $identity.Trim())
    $phase = 'Discover DirectShow cameras'
    $listing = Invoke-AgentProbe $ffmpeg @('-hide_banner', '-list_devices', 'true', '-f', 'dshow', '-i', 'dummy') -AllowFailure
    $devices = @(Read-DirectShowDevices $listing)
    Write-Host ('Parsed cameras: ' + $devices.Count)
    $reportDevices = @()
    foreach ($device in $devices | Select-Object -First 32) {
        $phase = 'Read camera quality modes'
        $caps = Invoke-AgentProbe $ffmpeg @('-hide_banner', '-list_options', 'true', '-f', 'dshow', '-i', "video=$($device.captureName)") -AllowFailure
        $modes = @(Read-DirectShowModes $caps | Select-Object -First 256)
        Write-Host ('Camera: {0}; Parsed quality modes: {1}' -f $device.name, $modes.Count)
        if ($modes.Count) { $reportDevices += [pscustomobject]@{ id=$device.id; name=$device.name; modes=$modes } }
    }
    $phase = 'Build inventory report'
    $report = @{ devices=@($reportDevices | Select-Object id,name,modes); status=@{} } | ConvertTo-Json -Depth 8 -Compress
    $phase = 'Poll Linux broker using the scheduled helper command'
    # Do not print the reply: an existing camera configuration can contain credentials.
    $reply = Invoke-AgentProbe 'wsl.exe' @('-d', $Distribution, '--exec', 'sentinel-usb', 'agent-poll') -InputText $report -TimeoutMs 15000
    $phase = 'Parse broker reply'
    $parsed = $reply | ConvertFrom-Json
    $null = $parsed.cameras
    Write-Host 'PASS: camera discovery and broker polling completed in this PowerShell session.'
    Write-Host 'This was one diagnostic poll, not proof that the scheduled helper stays connected.'
} catch {
    Write-Host ('FAILED STAGE: ' + $phase)
    Write-Host ('ERROR TYPE: ' + $_.Exception.GetType().FullName)
    # Script stack includes function/line locations but never camera credential payloads.
    Write-Host ('LOCATION: ' + $_.ScriptStackTrace)
    if ($phase -eq 'Read WSL identity') {
        # This fixed identity command cannot return camera publishing credentials.
        # Expose its error instead of hiding the actionable WSL failure message.
        Write-Host 'WSL identity output (through the helper process launcher):'
        try {
            Write-Host (Invoke-AgentProbe 'wsl.exe' @('-d', $Distribution, '--exec', 'id') -TimeoutMs 15000 -AllowFailure)
        } catch { Write-Host ('Identity probe could not finish: ' + $_.Exception.GetType().FullName) }
    }
    if ($phase -eq 'Poll Linux broker using the scheduled helper command') {
        Write-Host 'Checking the broker socket using the default WSL user (no root bypass)...'
        & wsl.exe -d $Distribution --exec ls -l /run/sentinel-usb-control/agent.sock
        & wsl.exe -d $Distribution --exec test -w /run/sentinel-usb-control/agent.sock
        Write-Host ('Socket permission check exit code: ' + $LASTEXITCODE)
    }
    # Suppress raw exception/command output because a malformed broker reply may include secrets.
    exit 1
}
