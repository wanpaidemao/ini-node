param(
    [string]$Level = 'warn'
)
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

[System.Net.ServicePointManager]::SecurityProtocol =
    [System.Net.SecurityProtocolType]::Tls12 -bor
    [System.Net.SecurityProtocolType]::Tls11
[System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
[System.Net.ServicePointManager]::CheckCertificateRevocationList = $false

$body = '{"jsonrpc":"1.0","id":"x","method":"debuglevel","params":["' + $Level + '"]}'
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
    Write-Output "HTTP $([int]$resp.StatusCode)"
    Write-Output "BODY: $out"
} catch [System.Net.WebException] {
    $r = $_.Exception.Response
    if ($r) {
        $sr = New-Object IO.StreamReader($r.GetResponseStream())
        Write-Output "HTTP $([int]$r.StatusCode)"
        Write-Output "BODY: $($sr.ReadToEnd())"
    } else {
        Write-Output "WEBEX: $($_.Exception.Message)"
    }
} catch {
    Write-Output "EX: $($_.Exception.Message)"
}
