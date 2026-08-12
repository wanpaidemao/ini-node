# mon-mem.ps1 - Poll header height + process memory every $Interval seconds
param(
    [int]$Interval = 30,
    [int]$DurationMinutes = 0,
    [string]$OutFile = "$PSScriptRoot\..\..\header-mem.csv"
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
        $req = [System.Net.HttpWebRequest]::Create("https://127.0.0.1:$RpcPort/")
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

$proc = Get-Process -Name 'btcd*' -ErrorAction SilentlyContinue | Sort-Object StartTime | Select-Object -First 1
if (-not $proc) { Write-Output "no btcd process found"; exit 1 }
$started = $proc.StartTime
if (-not (Test-Path $OutFile)) { Set-Content -Value "elapsed_min,header_tip,header_target,block_height,ibd,rss_mb,private_mb,peers" -Path $OutFile }

$endAt = if ($DurationMinutes -gt 0) { (Get-Date).AddMinutes($DurationMinutes) } else { $null }
while ($true) {
    $p = Get-Process -Id $proc.Id -ErrorAction SilentlyContinue
    if (-not $p) { Write-Output "btcd exited"; break }
    $el = [math]::Round(((Get-Date) - $started).TotalMinutes, 2)
    $sync = Invoke-Rpc 'getblocksyncstatus'
    $hdr = -1; $tg = -1; $ibd = '?'; $peers = 0
    if ($sync -notmatch 'ERR') {
        if ($sync -match '"header_tip":(-?\d+)') { $hdr = [int]$Matches[1] }
        if ($sync -match '"header_target":(-?\d+)') { $tg = [int]$Matches[1] }
        if ($sync -match '"ibd":(true|false)') { $ibd = $Matches[1] }
        $peers = ([regex]::Matches($sync, '"peer"|"addr"')).Count
    }
    $bc = Invoke-Rpc 'getblockcount'
    $h = if ($bc -match 'result":(-?\d+)') { [int]$Matches[1] } else { -1 }
    $rss = [math]::Round($p.WorkingSet64 / 1MB, 1)
    $priv = [math]::Round($p.PrivateMemorySize64 / 1MB, 1)
    Add-Content -Value "$el,$hdr,$tg,$h,$ibd,$rss,$priv,$peers" -Path $OutFile
    Write-Output "$(Get-Date -Format HH:mm:ss) el=${el}m header=$hdr target=$tg block=$h ibd=$ibd peers=$peers rss=${rss}MB"
    if ($endAt -and (Get-Date) -ge $endAt) { break }
    Start-Sleep -Seconds $Interval
}
