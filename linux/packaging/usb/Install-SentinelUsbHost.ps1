# One-time setup on the Windows PC hosting an existing Sentinel VMS in WSL2.
# Keep this file beside the rest of the extracted USB add-on package.
[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9_.-]+$')][string]$Distribution = 'Ubuntu',
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9_.-]*$')][string]$Container,
    [ValidateRange(0,2147483647)][int]$WebUid,
    [string]$ListenAddress
)
$ErrorActionPreference = 'Stop'
$webUidProvided = $PSBoundParameters.ContainsKey('WebUid')
. "$PSScriptRoot\WslUsb.Common.ps1"
. "$PSScriptRoot\WindowsAgent.Common.ps1"
foreach ($file in @('install-host.sh','install.sh','sentinel_usb.py','usb_control.py','sentinel-usb-control.service','sentinel-usb@.service','Install-SentinelWslUsb.ps1','Run-SentinelWindowsAgent.ps1','README.md')) {
    if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot $file))) { throw 'Extract the complete USB add-on package before running setup.' }
}
$kernel = Invoke-NativeText 'wsl.exe' @('-d',$Distribution,'--exec','uname','-r')
if ($kernel -notmatch '(?i)microsoft.*WSL2') { throw 'Select an existing WSL2 distribution. No host conversion or reboot was attempted.' }
# Use forward slashes and direct execution to preserve Windows paths, including spaces.
$linuxPath = (Invoke-NativeText 'wsl.exe' @('-d',$Distribution,'--exec','wslpath','-u',$PSScriptRoot.Replace('\','/'))).Trim()
$arguments = @('-d',$Distribution,'-u','root','--exec','bash',"$linuxPath/install-host.sh")
if ($Container) { $arguments += @('--container',$Container) }
if ($webUidProvided) { $arguments += @('--web-uid',[string]$WebUid) }
if ($ListenAddress) { $arguments += @('--listen-address',$ListenAddress) }
& wsl.exe @arguments
if ($LASTEXITCODE -ne 0) { throw 'Linux USB setup failed. Resolve the message above before retrying.' }
& "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PSScriptRoot\Install-SentinelWslUsb.ps1" -Backend WindowsCapture -Distribution $Distribution
if ($LASTEXITCODE -ne 0) { throw 'Linux setup completed, but Windows camera helper setup failed.' }
$online = $false
for ($attempt=0; $attempt -lt 20; $attempt++) {
    Start-Sleep -Seconds 3
    try {
        $reply = Invoke-AgentProbe 'wsl.exe' @('-d',$Distribution,'-u','root','--exec','curl','--fail','--silent','--max-time','5','--unix-socket','/run/sentinel-usb-control/control.sock','http://localhost/inventory') -TimeoutMs 10000
        $inventory = $reply | ConvertFrom-Json
        if ($inventory.windowsOnline) { $online = $true; break }
    } catch { } # Do not log protocol payloads.
}
if (-not $online) { throw 'Helper installed but not reporting to Linux. Run Test-SentinelWindowsHelper.ps1 from this package.' }
Write-Host 'Windows camera helper is reporting to Linux.'
$inventory.devices | Select-Object name,kind,@{Name='QualityModes';Expression={ @($_.modes).Count }} | Format-Table
Write-Host 'Open Admin > Cameras > Add USB camera. New cameras start stopped.'
Write-Host 'No capture requested. StarDesk, USB/IP, WSL and the VMS were not restarted.'
