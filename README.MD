# podman-pod-lab

Minimal URL shortener used to learn podman **pods**: a Go app and Redis
sharing one network namespace, talking over `localhost`, with a single
port published on the pod — the Kubernetes pod model, locally.

## Stack

- `main.go` — Go stdlib HTTP server + go-redis. `POST /shorten` stores a
  URL under a random hex code; `GET /<code>` 302-redirects.
- `container.dockerfile` — multi-stage build, static binary, `FROM scratch`.
- `shortlab.yaml` — Kubernetes Pod manifest (cleaned output of
  `podman generate kube`), used to recreate the whole stack declaratively.

## Run (imperative)

```sh
podman build --network=host -f container.dockerfile -t shortener .

podman pod create --name shortlab -p 8080:8080
podman run -d --pod shortlab --name shortlab-redis docker.io/library/redis:7-alpine
podman run -d --pod shortlab --name shortlab-app localhost/shortener
```

Note what's absent: no network creation, no links, no per-container ports.
The app reaches Redis at `localhost:6379` because pod members share one
network namespace. The port is published on the **pod** (held by the
infra container).

## Run (declarative)

```sh
podman play kube shortlab.yaml
```

Same YAML deploys to a real Kubernetes cluster if the image is pushed to
a registry.

## Configuration

Environment variables (all optional):

| Var | Default | Purpose |
|-----|---------|---------|
| `REDIS_ADDR` | `localhost:6379` | Redis address (localhost within a shared pod netns) |
| `PORT` | `8080` | HTTP listen port |
| `BASE_URL` | `http://localhost:$PORT` | Base for the short links returned by `/shorten` |

## Endpoints

- `POST /shorten` — body is a `http(s)://` URL; returns `BASE_URL/<code>`.
- `GET /<code>` — 302 redirect to the stored URL (404 if unknown).
- `GET /healthz` — 200 `ok` when Redis is reachable, 503 otherwise (use for
  container liveness/readiness probes).

The server enforces read/write/idle timeouts and shuts down gracefully on
SIGINT/SIGTERM, so in-flight requests finish when the pod stops.

## Use

```sh
curl -s -X POST -d "https://example.com" http://localhost:8080/shorten
# -> http://localhost:8080/ab12cd34
curl -iL http://localhost:8080/ab12cd34   # 302 -> original URL
```

Lifecycle: `podman pod stop|start|rm -f shortlab` operates on the whole
stack at once.

Redis has no AOF enabled — codes survive pod stop/start but not
rm/recreate (the anonymous `/data` PVC persists; add
`--appendonly yes` to redis args to actually use it).

## Gotchas (Windows / WSL2, podman 6.x)

Hard-won notes from getting `localhost:8080` reachable from Windows:

- **`podman machine` commands run on Windows only.** Inside WSL they try
  QEMU and fail. The machine is itself a WSL distro.
- **Builder Go version must match `go.mod`** (`go X.Y.Z` directive) —
  bump the `golang:` tag when the local toolchain moves.
- **`RUN` steps timing out on network** (proxy.golang.org unreachable):
  build with `--network=host`. Pull steps use different plumbing and
  can succeed while RUN networking is broken.
- **Rootless (pasta) port publishing was broken** on this setup — port
  claimed mapped, no listener anywhere. Fixed by rootful:
  `podman machine set --rootful` (check `podman system connection list`
  to see which socket is actually default).
- **Rootful publishes via nftables DNAT — no real socket.** So WSL's
  localhost relay never forwards it and `ss` shows nothing. Reach the
  service via the WSL eth0 IP, or add a `netsh interface portproxy`
  rule for a `localhost` URL.
- **Windows 11 Hyper-V firewall blocks host→WSL inbound by default**
  (all settings "NotConfigured" = block). Fix, elevated:

```powershell
  Set-NetFirewallHyperVVMSetting -Name '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' `
    -DefaultInboundAction Allow -LoopbackEnabled True
```

  Requires `wsl --shutdown` and machine restart to take effect.
- **WSL IP changes across reboots** — portproxy rules and saved IPs go
  stale. Check: `wsl -d podman-machine-default ip -4 addr show eth0`.