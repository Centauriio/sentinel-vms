$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\..\WslUsb.Common.ps1"
function Assert-Throws([scriptblock]$Action) {
    $threw = $false
    try { & $Action } catch { $threw = $true }
    if (-not $threw) { throw 'Expected a rejection.' }
}
$vendor = [pscustomobject]@{ProcessId=42; ExecutablePath='C:\Program Files\StarDesk\bin\server\usbipd.exe'; CommandLine='usbipd.exe server --usbipd:Port=3240'}
$listener = [pscustomobject]@{OwningProcess=42}
if ((Get-UsbProviderDecision @($listener) @($vendor)) -ne $vendor.ExecutablePath) { throw 'Must reuse active provider path.' }
if ($null -ne (Get-UsbProviderDecision @() @())) { throw 'No active provider should return null.' }
Assert-Throws { Get-UsbProviderDecision @() @($vendor) }
Assert-Throws { Get-UsbProviderDecision @($listener) @() }
Assert-Throws { Get-UsbProviderDecision @($listener, [pscustomobject]@{OwningProcess=43}) @($vendor) }
$hidden = [pscustomobject]@{ProcessId=42; ExecutablePath=$null; CommandLine=$null}
Assert-Throws { Get-UsbProviderDecision @($listener) @($hidden) }
$listing = "BUSID VID:PID DEVICE STATE`n1-4 32e4:4801 Camera Shared`n1-14 0cf3:e009 Bluetooth Not shared"
Assert-SelectedUsbDevice $listing '1-4' '32e4:4801'
Assert-Throws { Assert-SelectedUsbDevice $listing '1-14' '32e4:4801' }
Assert-Throws { Assert-SelectedUsbDevice $listing '1-5' '32e4:4801' }
Write-Output 'Provider ownership and selected-device guards passed (9 cases).'
