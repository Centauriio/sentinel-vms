[CmdletBinding()]
param([string]$Distribution='Ubuntu', [Parameter(Mandatory)][string]$FFmpeg)
$ErrorActionPreference='Stop'
. "$PSScriptRoot\WindowsAgent.Common.ps1"
$mutexName='Local\SentinelUsb-' + (Get-CameraIdentity $Distribution).Substring(8)
$mutex=New-Object Threading.Mutex($false,$mutexName)
if (-not $mutex.WaitOne(0)) { $mutex.Dispose(); exit 0 }
$running=@{}; $retry=@{}; $known=@(); $lastScan=[DateTime]::MinValue
$captureJob=Initialize-CaptureJob
try {
    while ($true) {
        try {
            if (([DateTime]::UtcNow-$lastScan).TotalSeconds -ge 30) {
                $listing=Invoke-AgentProbe $FFmpeg @('-hide_banner','-list_devices','true','-f','dshow','-i','dummy') -AllowFailure
                $found=@(Read-DirectShowDevices $listing)
                $next=@()
                foreach ($d in $found | Select-Object -First 32) {
                    # Reuse capabilities while capturing; probing an open camera can fail or interrupt it.
                    $old=@($known | Where-Object { $_.id -eq $d.id })
                    if ($old.Count -and @($running.Values | Where-Object { $_.device -eq $d.id }).Count) { $modes=$old[0].modes }
                    else {
                        $caps=Invoke-AgentProbe $FFmpeg @('-hide_banner','-list_options','true','-f','dshow','-i',"video=$($d.captureName)") -AllowFailure
                        $modes=@(Read-DirectShowModes $caps | Select-Object -First 256)
                    }
                    if ($modes.Count) { $next+= [pscustomobject]@{ id=$d.id; name=$d.name; captureName=$d.captureName; modes=$modes } }
                }
                $known=$next; $lastScan=[DateTime]::UtcNow
            }
            $status=@{}
            foreach ($id in @($running.Keys)) {
                if ($running[$id].process.HasExited) {
                    $running[$id].process.Dispose(); $running.Remove($id); $retry[$id]=[DateTime]::UtcNow.AddSeconds(10); $status[$id]='failed'
                } else { $status[$id]='running' }
            }
            foreach ($id in $retry.Keys) { if (-not $status.ContainsKey($id)) { $status[$id]='failed' } }
            $report=@{ devices=@($known | Select-Object id,name,modes); status=$status } | ConvertTo-Json -Depth 8 -Compress
            $answer=Invoke-AgentProbe 'wsl.exe' @('-d',$Distribution,'--','sentinel-usb','agent-poll') -InputText $report -TimeoutMs 8000
            $desired=($answer | ConvertFrom-Json).cameras
            foreach ($id in @($running.Keys)) {
                $match=@($desired | Where-Object { $_.instance -eq $id })
                $signature=if ($match.Count) { $match[0] | ConvertTo-Json -Depth 6 -Compress } else { '' }
                if (-not $match.Count -or $running[$id].signature -ne $signature) {
                    $running[$id].process.Kill(); $running[$id].process.WaitForExit(); $running[$id].process.Dispose(); $running.Remove($id)
                }
            }
            foreach ($camera in $desired) {
                $id=$camera.instance
                if ($running.ContainsKey($id) -or ($retry.ContainsKey($id) -and $retry[$id] -gt [DateTime]::UtcNow)) { continue }
                $device=@($known | Where-Object { $_.id -eq $camera.device })
                if (-not $device.Count) { continue }
                $arguments=Get-AgentCaptureArguments $camera $device[0].captureName
                $process=New-AgentProcess $FFmpeg $arguments -DiscardOutput
                try { $captureJob.Add($process.Handle) }
                catch { if (-not $process.HasExited) { $process.Kill() }; $process.Dispose(); throw }
                $running[$id]=@{ process=$process; device=$camera.device; signature=($camera | ConvertTo-Json -Depth 6 -Compress) }
                $retry.Remove($id)
            }
        } catch {
            # Fail closed if broker communication, discovery or desired state is invalid.
            foreach ($entry in $running.Values) { if (-not $entry.process.HasExited) { $entry.process.Kill() }; $entry.process.Dispose() }
            $running.Clear()
            # Do not write exception details: they may contain a publish URL.
        }
        Start-Sleep -Seconds 3
    }
} finally {
    foreach ($entry in $running.Values) { if (-not $entry.process.HasExited) { $entry.process.Kill() }; $entry.process.Dispose() }
    $mutex.ReleaseMutex(); $mutex.Dispose()
    $captureJob.Dispose()
}
