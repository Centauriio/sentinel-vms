# Windows-side prerequisite setup. Never replaces or stops a third-party provider.
[CmdletBinding()]
param(
    [ValidateSet('WindowsCapture', 'UsbIp')][string]$Backend = 'WindowsCapture',
    [string]$Distribution = 'Ubuntu',
    [ValidatePattern('^\d+-\d+(\.\d+)*$')][string]$BusId,
    [ValidatePattern('^[0-9a-fA-F]{4}:[0-9a-fA-F]{4}$')][string]$HardwareId
)
$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\WslUsb.Common.ps1"

$kernel = Invoke-NativeText 'wsl.exe' @('-d', $Distribution, '--', 'uname', '-r')
if ($kernel -notmatch '(?i)microsoft.*WSL2') { throw 'The selected distribution must be WSL2. No WSL update, shutdown or conversion was attempted.' }

if ($Backend -eq 'WindowsCapture') {
    $ffmpeg = Resolve-CaptureFFmpeg
    if (-not $ffmpeg) {
        if (-not (Get-Command winget.exe -ErrorAction SilentlyContinue)) { throw 'Install Microsoft App Installer (winget), then rerun.' }
        & winget.exe install --id Gyan.FFmpeg --exact --source winget --accept-source-agreements --accept-package-agreements
        if ($LASTEXITCODE -ne 0) { throw 'FFmpeg installation did not complete. No camera was started.' }
        $env:Path += ';' + [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
        $ffmpeg = Resolve-CaptureFFmpeg
    }
    if (-not $ffmpeg) { throw 'Open a new PowerShell window and rerun so PATH includes FFmpeg.' }
    Assert-CaptureFFmpeg $ffmpeg
    Write-Host 'Windows capture prerequisites are ready. USB/IP providers and drivers were left unchanged.'
    Write-Host 'Next: configure a windows-publisher camera with sentinel-usb in Ubuntu, then run Start-SentinelWindowsCamera.ps1.'
    exit 0
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run the UsbIp setup in an elevated PowerShell window to inspect provider ownership and bind the selected device.'
}
if ($BusId -and -not $HardwareId) { throw 'Provide -HardwareId VID:PID alongside -BusId to guard against bus ID changes.' }
if ($HardwareId -and -not $BusId) { throw '-HardwareId requires -BusId.' }
$provider = Get-ActiveUsbProvider
if (-not $provider) {
    $standalone = Join-Path $env:ProgramFiles 'usbipd-win\usbipd.exe'
    if (-not (Test-Path -LiteralPath $standalone)) {
        if (-not (Get-Command winget.exe -ErrorAction SilentlyContinue)) { throw 'Install Microsoft App Installer (winget), then rerun.' }
        # Interactive installation lets the user defer a required reboot.
        & winget.exe install --id dorssel.usbipd-win --exact --source winget --interactive --accept-source-agreements --accept-package-agreements
        if ($LASTEXITCODE -ne 0) { throw 'USB/IP installation did not complete; follow its installer instructions and rerun.' }
    }
    # Recheck ownership after installation; another app may have started its helper meanwhile.
    $provider = Get-ActiveUsbProvider
    if (-not $provider) {
        $service = Get-CimInstance Win32_Service -Filter "Name='usbipd'"
        if (-not $service -or $service.PathName -notlike ('*' + $standalone + '*')) {
            throw 'The usbipd service does not belong to the expected standalone installation. No service was started.'
        }
        Start-Service usbipd
        $provider = Get-ActiveUsbProvider
        if (-not $provider) { throw 'USB/IP service did not acquire TCP 3240. Inspect it before retrying.' }
    }
}
Write-Host "Using active USB/IP provider: $provider"
$listing = Invoke-NativeText $provider @('list')
if ($listing -match '(?im)\b(warning|error):') {
    Write-Host $listing
    throw 'The active provider reports a problem. Preserve its drivers/service; use -Backend WindowsCapture or repair that provider first.'
}
$helpText = Invoke-NativeText $provider @('attach', '--help')
if ($helpText -notmatch '--wsl' -or $helpText -notmatch '--busid') { throw 'Active provider lacks the supported WSL attach interface. Use WindowsCapture.' }
Write-Host $listing
if ($BusId) {
    Assert-SelectedUsbDevice $listing $BusId $HardwareId
    $null = Invoke-NativeText $provider @('bind', '--busid', $BusId)
    $listing = Invoke-NativeText $provider @('list')
    Assert-SelectedUsbDevice $listing $BusId $HardwareId
    $null = Invoke-NativeText $provider @('attach', '--wsl', $Distribution, '--busid', $BusId)
    Write-Host (Invoke-NativeText $provider @('list'))
    Write-Host 'Attach command completed. In Ubuntu run sudo sentinel-usb devices; capture also requires a working UVC/V4L2 driver.'
} else {
    Write-Host 'No device attached. Select the camera and rerun with -BusId and -HardwareId.'
}
