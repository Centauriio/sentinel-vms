Set-StrictMode -Version Latest

function Initialize-CaptureJob {
    # Kill only our FFmpeg children if Task Scheduler terminates the helper or it crashes.
    # A Windows job handle with KILL_ON_JOB_CLOSE does not affect other camera apps.
    if (-not ('SentinelCaptureJob' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public sealed class SentinelCaptureJob : IDisposable {
 [StructLayout(LayoutKind.Sequential)] struct Basic { public long ProcessTime,JobTime; public uint Flags; public UIntPtr Min,Max; public uint Active; public UIntPtr Affinity; public uint Priority,Scheduling; }
 [StructLayout(LayoutKind.Sequential)] struct IO { public ulong ReadOps,WriteOps,OtherOps,ReadBytes,WriteBytes,OtherBytes; }
 [StructLayout(LayoutKind.Sequential)] struct Extended { public Basic Basic; public IO IO; public UIntPtr ProcessMemory,JobMemory,PeakProcess,PeakJob; }
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr security,string name);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job,int type,IntPtr info,uint length);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job,IntPtr process);
 [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
 IntPtr handle;
 public SentinelCaptureJob() {
  handle=CreateJobObject(IntPtr.Zero,null); if(handle==IntPtr.Zero) throw new InvalidOperationException("Cannot create capture job");
  var info=new Extended(); info.Basic.Flags=0x2000;
  int size=Marshal.SizeOf(info); var data=Marshal.AllocHGlobal(size);
  try { Marshal.StructureToPtr(info,data,false); if(!SetInformationJobObject(handle,9,data,(uint)size)) { CloseHandle(handle); handle=IntPtr.Zero; throw new InvalidOperationException("Cannot protect capture job"); } }
  finally { Marshal.FreeHGlobal(data); }
 }
 public void Add(IntPtr process) { if(!AssignProcessToJobObject(handle,process)) throw new InvalidOperationException("Cannot assign capture job"); }
 public void Dispose() { if(handle!=IntPtr.Zero) { CloseHandle(handle); handle=IntPtr.Zero; } }
}
'@
    }
    return New-Object SentinelCaptureJob
}

function ConvertTo-NativeArgument {
    param([AllowEmptyString()][string]$Value)
    # CommandLineToArgvW quoting, including embedded quotes and trailing backslashes.
    return '"' + [regex]::Replace([regex]::Replace($Value, '(\\*)"', '$1$1\"'), '(\\+)$', '$1$1') + '"'
}

function New-AgentProcess {
    param([string]$Executable, [string[]]$Arguments, [switch]$DiscardOutput)
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Executable
    $info.Arguments = ($Arguments | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.RedirectStandardInput = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $info
    [void]$process.Start()
    if ($DiscardOutput) {
        # Drain without retaining or logging FFmpeg output (it can contain publishing credentials).
        $process.BeginOutputReadLine()
        $process.BeginErrorReadLine()
        $process.StandardInput.Close()
    }
    return $process
}

function Invoke-AgentProbe {
    param([string]$Executable, [string[]]$Arguments, [string]$InputText = '', [int]$TimeoutMs = 10000, [switch]$AllowFailure)
    $process = New-AgentProcess $Executable $Arguments
    try {
        $output = $process.StandardOutput.ReadToEndAsync()
        $errors = $process.StandardError.ReadToEndAsync()
        $process.StandardInput.Write($InputText)
        $process.StandardInput.Close()
        if (-not $process.WaitForExit($TimeoutMs)) { $process.Kill(); throw 'Camera helper command timed out.' }
        if (-not $AllowFailure -and $process.ExitCode -ne 0) { throw 'Camera helper command failed.' }
        return $output.Result + $errors.Result
    } finally { $process.Dispose() }
}

function Get-CameraIdentity {
    param([string]$AlternativeName)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return 'windows:' + ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($AlternativeName))).Replace('-', '').ToLowerInvariant()).Substring(0,32) }
    finally { $sha.Dispose() }
}

function Read-DirectShowDevices {
    param([string]$Text)
    $name = $null
    foreach ($line in $Text -split "`n") {
        if ($line -match '"(.*)" \(video\)') { $name = $Matches[1] }
        elseif ($name -and $line -match 'Alternative name "(.*)"') {
            [pscustomobject]@{ id=(Get-CameraIdentity $Matches[1]); name=$name; captureName=$Matches[1] }
            $name = $null
        }
    }
}

function Read-DirectShowModes {
    param([string]$Text)
    $seen = @{}
    foreach ($line in $Text -split "`n") {
        if ($line -notmatch '(?:vcodec|pixel_format)=(mjpeg|yuyv422|nv12)\s+min s=(\d+)x(\d+) fps=([\d.]+) max s=(\d+)x(\d+) fps=([\d.]+)') { continue }
        $format=$Matches[1]; $width=[int]$Matches[2]; $height=[int]$Matches[3]
        $fps=[double]::Parse($Matches[4], [Globalization.CultureInfo]::InvariantCulture)
        # Discrete advertised modes only. Never infer a resolution or frame rate from a range.
        if ($Matches[2] -ne $Matches[5] -or $Matches[3] -ne $Matches[6] -or $Matches[4] -ne $Matches[7]) { continue }
        if ($width -lt 160 -or $width -gt 7680 -or $height -lt 120 -or $height -gt 4320 -or $width % 2 -or $height % 2 -or $fps -lt 1 -or $fps -gt 60) { continue }
        $key="$format/$width/$height/$fps"
        if (-not $seen.ContainsKey($key)) {
            $seen[$key]=$true
            [pscustomobject]@{ format=$format; width=$width; height=$height; fps=$fps }
        }
    }
}

function Get-AgentCaptureArguments {
    param($Camera, [string]$CaptureName)
    if ($Camera.instance -notmatch '^web-[a-f0-9]{24}$') { throw 'Invalid camera identifier.' }
    $target=[Uri]$Camera.url
    $address=$null
    if ($target.Scheme -ne 'rtsp' -or $target.UserInfo -notmatch '^publisher:[a-f0-9]{48}$' -or
        -not [Net.IPAddress]::TryParse($target.DnsSafeHost, [ref]$address) -or $target.AbsolutePath -ne '/camera') { throw 'Invalid publishing connection.' }
    $p=$Camera.profile
    if ($p.format -notin @('mjpeg','yuyv422','nv12') -or $p.width -lt 160 -or $p.width -gt 7680 -or $p.height -lt 120 -or $p.height -gt 4320 -or $p.fps -lt 1 -or $p.fps -gt 60 -or $p.bitrate -lt 128 -or $p.bitrate -gt 50000) { throw 'Invalid quality settings.' }
    $fps=[Convert]::ToString($p.fps,[Globalization.CultureInfo]::InvariantCulture)
    $argsList=@('-hide_banner','-nostdin','-loglevel','warning','-f','dshow','-rtbufsize','128M','-video_size',"$($p.width)x$($p.height)",'-framerate',$fps)
    if ($p.format -eq 'mjpeg') { $argsList+=@('-vcodec','mjpeg') } else { $argsList+=@('-pixel_format',$p.format) }
    $argsList+=@('-i',"video=$CaptureName",'-an')
    if ($p.format -eq 'mjpeg') { $argsList+=@('-vf','scale=in_range=full:out_range=limited','-color_range','tv') }
    return $argsList+@('-c:v','libx264','-preset','veryfast','-tune','zerolatency','-pix_fmt','yuv420p','-b:v',"$($p.bitrate)k",'-maxrate',"$($p.bitrate)k",'-bufsize',"$($p.bitrate*2)k",'-g',[string][Math]::Round($p.fps*2),'-sc_threshold','0','-fps_mode','passthrough','-f','rtsp','-rtsp_transport','tcp',$Camera.url)
}
