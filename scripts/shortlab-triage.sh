#!/usr/bin/env bash
# Triage reachability of the shortlab pod from WSL / Linux.
#
# From a WSL shell the pod is reachable natively on http://localhost:PORT
# (the podman machine's published port is visible in the WSL network
# namespace), so unlike the Windows side there is no portproxy to manage.
# This script just verifies the chain and prints how to reach it.
#
# Usage: ./shortlab-triage.sh [PORT]   (default 8081)
set -u
PORT="${1:-8081}"
MACHINE="podman-machine-default"
POD="shortlab"
ok=0

good() { printf '  [ok]   %s\n' "$1"; }
bad()  { printf '  [FAIL] %s\n' "$1"; ok=1; }
info() { printf '  [info] %s\n' "$1"; }

echo "== shortlab reachability triage (port $PORT) =="

# 1. machine running?
if podman machine list --format '{{.Name}} {{.LastUp}}' 2>/dev/null | grep -q "$MACHINE.*Currently running"; then
  good "podman machine '$MACHINE' is running"
else
  bad "podman machine '$MACHINE' is not running (podman machine start)"
fi

# 2. pod up?
status="$(podman pod ps --filter "name=$POD" --format '{{.Status}}' 2>/dev/null)"
if printf '%s' "$status" | grep -q Running; then
  good "pod '$POD' is Running"
else
  bad "pod '$POD' status: '${status:-not found}' (podman play kube shortlab.yaml)"
fi

# 3. serving inside the machine?
inside="$(podman machine ssh "curl -s -o /dev/null -w '%{http_code}' http://localhost:$PORT/ 2>/dev/null" 2>/dev/null)"
if printf '%s' "$inside" | grep -qE '^[23]'; then
  good "serving inside the machine (HTTP $inside)"
else
  bad "not serving inside the machine (got '${inside:-none}') - app is down"
fi

# 4. reachable from this WSL shell?
here="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://localhost:$PORT/" 2>/dev/null)"
if printf '%s' "$here" | grep -qE '^[234]'; then
  good "reachable from WSL: http://localhost:$PORT/ (HTTP $here)"
else
  bad "not reachable from WSL on http://localhost:$PORT/ (got '${here:-none}')"
  info "if the machine is up and serving, check WSL<->machine networking"
fi

echo
if [ "$ok" -eq 0 ]; then
  echo "All good. WSL: http://localhost:$PORT"
  echo "For Windows, run shortlab-triage.ps1 -Fix (elevated) to refresh the portproxy."
else
  echo "Issues above - fix the first FAIL and re-run."
fi
exit "$ok"