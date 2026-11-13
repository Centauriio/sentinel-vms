Set-StrictMode -Version Latest

function Get-UsbProviderDecision {
    param([object[]]$Listeners, [object[]]$Processes)
    $owners = @($Listeners | Select-Object -ExpandProperty OwningProcess -Unique)
    $servers = @($Processes | Where-Object { $_.CommandLine -match '(?i)\bserver\b' })
    if ($owners.Count -gt 1) { throw 'Multiple USB/IP listeners found. Resolve provider ownership before attaching.' }
    if ($owners.Count -eq 0) {
        if ($servers.Count -gt 0) { throw 'A USB/IP helper is running without the expected TCP 3240 listener. Preserve it and use WindowsCapture or repair that provider.' }
        return $null
    }
    $owner = @($Processes | Where-Object { $_.ProcessId -eq $owners[0] })
    if ($owner.Count -ne 1 -or -not $owner[0].ExecutablePath -or
        [IO.Path]::GetFileName($owner[0].ExecutablePath) -ine 'usbipd.exe') {
        throw 'TCP 3240 is owned by an unknown or inaccessible process. No provider will be installed or stopped.'
    }
    return $owner[0].ExecutablePath
}

function Get-ActiveUsbProvider {
    # Admin access is required to reliably inspect another user's/service's executable path.
    $listeners = @(Get-NetTCPConnection -LocalPort 3240 -State Listen -ErrorAction SilentlyContinue)
    $processes = @(Get-CimInstance Win32_Process -Filter "Name='usbipd.exe'" -ErrorAction Stop)
    return Get-UsbProviderDecision -Listeners $listeners -Processes $processes
}

function Assert-SelectedUsbDevice {
    param([string]$Listing, [string]$BusId, [string]$HardwareId)
    $pattern = '(?im)^\s*' + [regex]::Escape($BusId) + '\s+([0-9a-f]{4}:[0-9a-f]{4})\s+'
    $match = [regex]::Match($Listing, $pattern)
    if (-not $match.Success -or $match.Groups[1].Value -ine $HardwareId) {
        throw 'Selected bus ID is missing or now belongs to a different device. List devices again; nothing was attached.'
    }
}

function Resolve-CaptureFFmpeg {
    $found = Get-Command ffmpeg.exe -ErrorAction SilentlyContinue
    if ($found) { return $found.Source }
    foreach ($candidate in @(
        "$env:LOCALAPPDATA\Microsoft\WinGet\Links\ffmpeg.exe",
        "$env:ProgramFiles\WinGet\Links\ffmpeg.exe"
    )) { if (Test-Path -LiteralPath $candidate) { return $candidate } }
    return $null
}

function Invoke-NativeText {
    param([string]$Executable, [string[]]$Arguments)
    # Windows PowerShell 5.1 represents native stderr as ErrorRecord; do not throw on informational stderr.
    $ErrorActionPreference = 'Continue'
    $PSNativeCommandUseErrorActionPreference = $false
    $lines = & $Executable @Arguments 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0) { throw "Prerequisite command failed (exit $code)." }
    return ($lines | ForEach-Object { $_.ToString() }) -join "`n"
}

function Assert-CaptureFFmpeg {
    param([string]$Path)
    $devices = Invoke-NativeText $Path @('-hide_banner', '-devices')
    $encoders = Invoke-NativeText $Path @('-hide_banner', '-encoders')
    if ($devices -notmatch '\bdshow\b' -or $encoders -notmatch '\blibx264\b') {
        throw 'FFmpeg must support DirectShow input and libx264 encoding. Install an encoding-capable Windows build.'
    }
}
