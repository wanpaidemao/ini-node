$ErrorActionPreference = 'Continue'
$IniPath = Join-Path $PSScriptRoot '..\btcd-runtime.ini'
function Get-RpcIni {
    param([string]$key, [string]$def)
    if (-not (Test-Path $IniPath)) { return $def }
    foreach ($l in Get-Content $IniPath) {
        if ($l.Trim() -match "^$key=(.*)$") { return $Matches[1] }
    }
    return $def
}
$user        = Get-RpcIni 'rpcuser' 'user'
$pass        = Get-RpcIni 'rpcpass' 'pass'
$rpclisten   = Get-RpcIni 'rpclisten' '127.0.0.1:8334'
$rpcPort     = ([string]$rpclisten).Split(':')[-1]

function Invoke-Rpc([string]$method) {
    [System.Net.ServicePointManager]::SecurityProtocol =
        [System.Net.SecurityProtocolType]::Tls12 -bor
        [System.Net.SecurityProtocolType]::Tls11
    [System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
    [System.Net.ServicePointManager]::CheckCertificateRevocationList = $false

    $body = "{`"jsonrpc`":`"1.0`",`"id`":`"x`",`"method`":`"$method`"}"
    $auth = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes("$user`:$pass"))
    try {
$req = [System.Net.HttpWebRequest]::Create("https://127.0.0.1:$rpcPort/")
        $req.Method = 'POST'
        $req.ContentType = 'application/json'
        $req.Accept = 'application/json'
        $req.Timeout = 30000
        $req.Headers.Add('Authorization', "Basic $auth")
        $b = [Text.Encoding]::UTF8.GetBytes($body)
        $req.ContentLength = $b.Length
        $s = $req.GetRequestStream()
        $s.Write($b, 0, $b.Length)
        $s.Close()
        $resp = $req.GetResponse()
        $sr = New-Object IO.StreamReader($resp.GetResponseStream())
        $out = $sr.ReadToEnd()
        $sr.Close()
        $resp.Close()
        return $out
    } catch {
        return "ERR: $($_.Exception.Message)"
    }
}

$proc = Get-Process -Name 'ini*' -ErrorAction SilentlyContinue | Sort-Object StartTime | Select-Object -First 1
if ($proc) {
    $cpu1 = $proc.CPU
    $uptime = (Get-Date) - $proc.StartTime
} else {
    $cpu1 = $null
    $uptime = $null
}

Start-Sleep -Seconds 2

if ($proc) {
    $proc = Get-Process -Id $proc.Id -ErrorAction SilentlyContinue
    $cpu2 = $proc.CPU
    $pct = if ($cpu1 -ne $null -and $cpu2 -ne $null) {
        [math]::Round((($cpu2 - $cpu1) / 2) * 100 / [Environment]::ProcessorCount, 1)
    } else { 0 }
} else {
    $pct = 0
}

$raw = Invoke-Rpc 'getblockcount'
$h = if ($raw -match 'result":(\d+)') { [int]$matches[1] } else { -1 }

if ($proc) {
    Write-Output ("Process : {0} (PID {1})" -f $proc.ProcessName, $proc.Id)
    if ($uptime) { Write-Output ("Uptime  : {0:N1} minutes" -f $uptime.TotalMinutes) }
    Write-Output ("CPU     : {0}% (last 2s)" -f $pct)
} else {
    Write-Output "Process : (none)"
}
Write-Output ("Height  : {0}" -f $h)
