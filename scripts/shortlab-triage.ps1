#!/usr/bin/env pwsh
<#
.SYNOPSIS
  Triage + repair reachability of the shortlab pod from Windows.

.DESCRIPTION
  The shortlab URL-shortener runs as a rootful podman pod inside the
  `wsl`-type podman machine. It publishes host:8081 -> container:8080 via
  netavark, which Windows cannot reach directly (rootful netavark DNAT does
  not accept raw external hits). A netsh portproxy bridges
  127.0.0.1:8081 -> <machine-ip>:8081, but the machine IP changes on every
  machine restart, so the proxy goes stale.

  This script checks the whole chain in order and fixes only what is broken:
    1. podman machine running?
    2. shortlab pod up and serving 200 inside the machine?
    3. current machine IP
    4. portproxy present and pointing at the current IP? (repoint if not)
    5. reachable from Windows on http://localhost:PORT ?

  Run from an ELEVATED PowerShell (netsh portproxy edits require admin).

.PARAMETER Port
  Host port the pod publishes on. Default 8081.

.PARAMETER Fix
  Apply fixes (start machine, (re)create the portproxy). Without -Fix the
  script only reports state.
#>
[CmdletBinding()]
param(
    [int]$Port = 8081,
    [switch]$Fix
)

$ErrorActionPreference = 'Stop'
$machine = 'podman-machine-default'
$pod = 'shortlab'
$ok = $true

function Say([string]$s, [string]$color = 'Gray') { Write-Host $s -ForegroundColor $color }
function Good([string]$s) { Say "  [ok]   $s" 'Green' }
function Bad ([string]$s) { Say "  [FAIL] $s" 'Red'; $script:ok = $false }
function Info([string]$s) { Say "  [info] $s" 'Cyan' }

Say "== shortlab reachability triage (port $Port) ==" 'White'

# --- 1. machine running? -----------------------------------------------
$running = $false
try {
    $list = podman machine list --format '{{.Name}} {{.LastUp}}' 2>$null
    if ($list -match "$machine.*Currently running") { $running = $true }
}
catch { }

if ($running) {
    Good "podman machine '$machine' is running"
}
else {
    Bad "podman machine '$machine' is not running"
    if ($Fix) {
        Info 'starting machine...'
        podman machine start | Out-Null
        Start-Sleep -Seconds 3
        $running = $true
        Good 'machine started'
    }
    else {
        Say "`nRun with -Fix to start it, or: podman machine start" 'Yellow'
        return
    }
}

# --- 2. pod up + serving inside? ---------------------------------------
$podStatus = (podman pod ps --filter "name=$pod" --format '{{.Status}}' 2>$null)
if ($podStatus -match 'Running') {
    Good "pod '$pod' is Running"
}
else {
    Bad "pod '$pod' status: '$podStatus' (expected Running)"
    Info 'bring it up from the quadlet dir: podman play kube shortlab.yaml'
    Info "(or if it exists but is stopped: podman pod start $pod)"
    # don't auto-recreate: we don't know the yaml path from here
}

$inside = (podman machine ssh "curl -s -o /dev/null -w '%{http_code}' http://localhost:$Port/ 2>/dev/null" 2>$null)
if ($inside -eq '200' -or $inside -match '^[23]') {
    Good "serving inside the machine (HTTP $inside on localhost:$Port)"
}
else {
    Bad "not serving inside the machine (got '$inside') - the app itself is down"
}

# --- 3. current machine IP ---------------------------------------------
$ip = (podman machine ssh "ip -4 addr show eth0 | grep -oP '(?<=inet\s)\d+(\.\d+){3}'" 2>$null).Trim()
if ($ip) {
    Good "machine eth0 IP: $ip"
}
else {
    Bad 'could not determine machine IP'
    return
}

# --- 4. portproxy present + correct? -----------------------------------
$proxies = netsh interface portproxy show v4tov4
$line = $proxies | Select-String "127\.0\.0\.1\s+$Port\s+"
$needsFix = $true
if ($line) {
    if ($line -match [regex]::Escape($ip)) {
        Good "portproxy 127.0.0.1:$Port -> $ip`:$Port (current)"
        $needsFix = $false
    }
    else {
        Bad "portproxy exists but points at a stale IP (not $ip)"
    }
}
else {
    Bad "no portproxy for 127.0.0.1:$Port"
}

if ($needsFix) {
    if ($Fix) {
        Info "repointing portproxy -> $ip`:$Port ..."
        netsh interface portproxy delete v4tov4 listenaddress=127.0.0.1 listenport=$Port 2>$null | Out-Null
        netsh interface portproxy add v4tov4 listenaddress=127.0.0.1 listenport=$Port connectaddress=$ip connectport=$Port | Out-Null
        Good 'portproxy set'
    }
    else {
        Say "`nRun with -Fix to (re)create the portproxy." 'Yellow'
    }
}

# --- 5. reachable from Windows? ----------------------------------------
try {
    $resp = Invoke-WebRequest -Uri "http://localhost:$Port/" -TimeoutSec 5 -UseBasicParsing -ErrorAction Stop
    Good "reachable from Windows: http://localhost:$Port/ (HTTP $($resp.StatusCode))"
}
catch {
    # a 4xx still proves reachability (the app answered)
    if ($_.Exception.Response) {
        Good "reachable from Windows: http://localhost:$Port/ (app responded)"
    }
    else {
        Bad "NOT reachable from Windows on http://localhost:$Port/ : $($_.Exception.Message)"
    }
}

Say ''
if ($ok) {
    Say "All good. Windows: http://localhost:$Port  |  WSL: http://localhost:$Port" 'Green'
}
else {
    Say "Issues above. Re-run elevated with -Fix to repair what's fixable." 'Yellow'
}