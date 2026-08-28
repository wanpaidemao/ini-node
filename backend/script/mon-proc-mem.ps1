# mon-proc-mem.ps1 - Sample btcd process memory (+ header height) every minute.
param(
    [int]$IntervalSec = 60,
    [string]$OutFile = "$PSScriptRoot\..\..\proc-mem.csv"
)
$ErrorActionPreference = 'Continue'

# Credentials/port come from btcd-runtime.ini (same source as the other scripts).
$IniPath = Join-Path $PSScriptRoot '..\btcd-runtime.ini'
function Get-RpcIni {
    param([string]$key, [string]$def)
    if (-not (Test-Path $IniPath)) { return $def }
    foreach ($l in Get-Content $IniPath) {
        if ($l.Trim() -match "^$key=(.*)$") { return $Matches[1] }
    }
    return $def
}
$RpcUser = Get-RpcIni 'rpcuser' 'user'
$RpcPass = Get-RpcIni 'rpcpass' 'pass'
$RpcPort = [int]((Get-RpcIni 'rpclisten' '127.0.0.1:8334').Split(':')[-1])

function Invoke-Rpc([string]$method) {
    [System.Net.ServicePointManager]::SecurityProtocol =
        [System.Net.SecurityProtocolType]::Tls12 -bor
        [System.Net.SecurityProtocolType]::Tls11
    [System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
    [System.Net.ServicePointManager]::CheckCertificateRevocationList = $false
    $body = "{`"jsonrpc`":`"1.0`",`"id`":`"x`",`"method`":`"$method`"}"
    $auth = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes("$RpcUser`:$RpcPass"))
    try {
        $req = [System.Net.HttpWebRequest]::Create("http://127.0.0.1:$RpcPort/")
        $req.Method = 'POST'
        $req.ContentType = 'application/json'
        $req.Accept = 'application/json'
        $req.Timeout = 10000
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
    } catch { return 'ERR' }
}

$proc = Get-Process -Name 'ini*' -ErrorAction SilentlyContinue | Sort-Object StartTime | Select-Object -First 1
if (-not $proc) { Write-Output "no ini process found"; exit 1 }
$started = $proc.StartTime

if (-not (Test-Path $OutFile)) {
    Set-Content -Value "timestamp,elapsed_min,rss_mb,private_mb,ws_mb,paged_mb,header_tip,block_height,header_rate_hps" -Path $OutFile
}

$prevHdr = -1
$prevTime = $null
while ($true) {
    $p = Get-Process -Id $proc.Id -ErrorAction SilentlyContinue
    if (-not $p) { Write-Output "$(Get-Date -Format 'HH:mm:ss') ini exited"; break }
    $el = [math]::Round(((Get-Date) - $started).TotalMinutes, 2)
    $sync = Invoke-Rpc 'getblocksyncstatus'
    $hdr = -1
    if ($sync -notmatch 'ERR' -and $sync -match '"header_tip":(-?\d+)') { $hdr = [int]$Matches[1] }
    $bc = Invoke-Rpc 'getblockcount'
    $h = if ($bc -match 'result":(-?\d+)') { [int]$Matches[1] } else { -1 }

    $rate = 0.0
    if ($prevHdr -gt 0 -and $hdr -gt 0 -and $prevTime) {
        $dt = ((Get-Date) - $prevTime).TotalSeconds
        if ($dt -gt 0 -and $hdr -ge $prevHdr) { $rate = [math]::Round(($hdr - $prevHdr) / $dt, 1) }
    }
    $prevHdr = $hdr
    $prevTime = Get-Date

    $rss   = [math]::Round($p.WorkingSet64 / 1MB, 1)
    $priv  = [math]::Round($p.PrivateMemorySize64 / 1MB, 1)
    $ws    = [math]::Round($p.WorkingSet64 / 1MB, 1)
    $paged = [math]::Round($p.PagedMemorySize64 / 1MB, 1)
    Add-Content -Value "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss'),$el,$rss,$priv,$ws,$paged,$hdr,$h,$rate" -Path $OutFile
    Write-Output ("{0} el={1}m rss={2}MB priv={3}MB header={4} block={5} rate={6}h/s" -f (Get-Date -Format 'HH:mm:ss'), $el, $rss, $priv, $hdr, $h, $rate)
    Start-Sleep -Seconds $IntervalSec
}
