[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^[a-z][a-z0-9-]{0,31}$')][string]$Name,
    [string]$Distribution = 'Ubuntu'
)
$urlLines = & wsl.exe -d $Distribution -- sudo -- sentinel-usb url $Name
if ($LASTEXITCODE -ne 0) { throw 'Could not read the camera URL.' }
$url = ($urlLines -join "`n").Trim()
if ($url -notmatch '^rtsp://reader:[a-f0-9]{48}@') { throw 'Unexpected camera URL; clipboard unchanged.' }
try {
    Set-Clipboard -Value $url -ErrorAction Stop
    Write-Host 'Camera URL copied. Paste only into Sentinel, then clear the clipboard.'
} finally {
    $urlLines = $null
    $url = $null
}
