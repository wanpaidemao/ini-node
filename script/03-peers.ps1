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

$raw = Invoke-Rpc 'getpeerinfo'
if ($raw -notmatch '"result":\[') {
    Write-Output $raw
    exit 1
}

$json = ($raw -replace '^.*?"result":', '{"result":' -replace '"error":.*$', '}')
$json = [regex]::Match($raw, '"result":(\[.*?\]),\s*"error"').Groups[1].Value
$peers = @()
foreach ($m in [regex]::Matches($json, '\{(?:[^{}])*\}')) {
    $obj = $m.Value | ConvertFrom-Json
    if ($obj) { $peers += $obj }
}

Write-Output ("Connected peers: {0}" -f $peers.Count)
Write-Output ""
$i = 0
foreach ($p in $peers) {
    $i++
    $sync = if ($p.syncnode) { ' *SYNC*' } else { '' }
    Write-Output ("[{0}] {1}{2}" -f $i, $p.addr, $sync)
    Write-Output ("    subver  : {0}" -f $p.subver)
    Write-Output ("    height  : {0}  services: {1}" -f $p.startingheight, $p.services)
    Write-Output ("    recv/send: {0}/{1} bytes  lastrecv: {2}" -f $p.bytesrecv, $p.bytessent, $p.lastrecv)
    Write-Output ("    conn    : {0}  ping: {1}" -f $p.conntime, $p.pingtime)
    Write-Output ""
}
