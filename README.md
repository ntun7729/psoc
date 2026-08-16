# psoc

`psoc` is a small Go-based HTTP/SOCKS proxy checker with an embedded web dashboard. It is designed for explicit proxy lists you provide, not CIDR sweeps or Internet-wide discovery.

## Features

- HTTP CONNECT / HTTP proxy checks
- SOCKS5 and SOCKS4/SOCKS4a checks
- Embedded dashboard and JSON API
- One-shot CLI output as JSON, CSV, or text
- Alpine-friendly static Go binary
- Multi-stage Docker image and Compose example
- Manual GitHub Actions proxy-check workflow
- Concurrency, timeout, and target-count limits
- Private/local target IPs blocked by default
- Optional bearer token for the scan API
- Result persistence to a JSON file

## Quick start

### Local / Alpine

Requires Go 1.23+ to build.

```sh
go build -trimpath -o psoc .
./psoc serve --listen 127.0.0.1:8080
```

Open `http://127.0.0.1:8080`.

On Alpine, the built binary has no libc dependency when built with `CGO_ENABLED=0`:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o psoc .
./psoc serve
```

### Docker Compose

```sh
docker compose up --build -d
```

The example publishes only to `127.0.0.1:8080` on the host.

### Docker

```sh
docker build -t psoc .
docker run --rm \
  -p 127.0.0.1:8080:8080 \
  -v psoc-data:/data \
  psoc
```

If you intentionally expose the dashboard on a wider network, set an API token:

```sh
docker run --rm \
  -p 8080:8080 \
  -e PSOC_API_TOKEN='replace-with-a-long-random-token' \
  -v psoc-data:/data \
  psoc
```

The dashboard has an API-token field and sends the value as a bearer token for scan requests.

## CLI scan

Accepted input formats:

```text
203.0.113.10:8080
http://203.0.113.11:3128
socks5://203.0.113.12:1080
socks4://203.0.113.13:1080
```

A bare `host:port` is checked as HTTP, SOCKS5, and SOCKS4 unless `--protocols` narrows it.

```sh
./psoc scan \
  --input targets.txt \
  --output results.json \
  --format json \
  --protocols http,socks5,socks4 \
  --concurrency 64 \
  --timeout 8s
```

Use stdin/stdout with `-`:

```sh
printf '%s\n' '203.0.113.10:8080' | ./psoc scan --input - --output - --format text
```

## Verification behavior

By default, each candidate proxy is asked to fetch `https://example.com/`.

- HTTP candidates use `CONNECT` for HTTPS verification.
- SOCKS5 uses the no-auth CONNECT handshake.
- SOCKS4 uses SOCKS4 or SOCKS4a depending on the destination host.
- HTTP status `200-399` is considered alive.

Override the destination with `--verify-url` / `PSOC_VERIFY_URL`. For repeatable or high-volume use, point this at an HTTP(S) endpoint you control.

## Safety boundaries

The checker intentionally accepts only explicit `host:port` targets. It has no CIDR expansion, subnet sweep, port-range scan, or Internet-discovery mode.

By default it blocks loopback, RFC1918/private, link-local, multicast, and other non-public target addresses. `--allow-private` exists only for testing infrastructure you control.

Default limits:

- concurrency: 64, hard-capped at 256
- timeout: 8 seconds, hard-capped at 60 seconds
- targets per scan: 5,000, hard-capped at 50,000
- GitHub Actions manual workflow: 500 targets, concurrency 32

## Server configuration

| Variable | Default | Purpose |
|---|---:|---|
| `PSOC_LISTEN` | `127.0.0.1:8080` | dashboard/API bind address |
| `PSOC_DATA` | `./data/results.json` | persisted results |
| `PSOC_CONCURRENCY` | `64` | concurrent checks |
| `PSOC_TIMEOUT` | `8s` | per-candidate timeout |
| `PSOC_VERIFY_URL` | `https://example.com/` | URL fetched through the proxy |
| `PSOC_MAX_TARGETS` | `5000` | max explicit targets per scan |
| `PSOC_ALLOW_PRIVATE` | `false` | permit private/local target IPs |
| `PSOC_API_TOKEN` | empty | bearer token required by `POST /api/scan` |
| `PSOC_TARGETS_FILE` | empty | optional file to scan once at startup |

## API

- `GET /healthz`
- `GET /api/stats`
- `GET /api/proxies`
- `GET /api/status`
- `POST /api/scan`

Example scan request:

```sh
curl -X POST http://127.0.0.1:8080/api/scan \
  -H 'Content-Type: application/json' \
  -d '{"targets":"203.0.113.10:8080\nsocks5://203.0.113.11:1080","protocols":["http","socks5"]}'
```

With `PSOC_API_TOKEN` set, add `Authorization: Bearer <token>`.

## GitHub Actions

- `CI` runs tests, vet, a native build, and a Docker build.
- `Manual proxy check` is `workflow_dispatch` only. It checks the explicit targets entered for that run and uploads `results.json` as a 7-day artifact.

There is deliberately no scheduled or automatic public scanning workflow.
