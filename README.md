# psoc

`psoc` is a small Go-based HTTP/SOCKS proxy checker with an embedded web dashboard. It can fetch a curated set of public proxy feeds, de-duplicate them, and independently verify the proxies from the machine running `psoc`.

## Features

- HTTP CONNECT / HTTP proxy checks
- SOCKS5 and SOCKS4/SOCKS4a checks
- Curated public HTTP/SOCKS proxy sources with feed-health reporting
- Embedded dashboard and JSON API
- One-shot CLI output as JSON, CSV, or text
- Static Linux binaries for amd64 and arm64
- Binary-only Alpine Docker image packaging
- Manual GitHub Actions proxy-check workflow
- Concurrency, timeout, source-size, and target-count limits
- Private/local target IPs blocked by default
- Optional bearer token for write APIs
- Result persistence to a JSON file

See `SOURCES.md` for the bundled public-list providers.

## Prebuilt binaries

The `CI` GitHub Actions workflow builds and uploads two static artifacts:

- `psoc-linux-amd64`
- `psoc-linux-arm64`

Run the workflow manually or use an artifact from a successful branch/PR run. After downloading the binary for your machine:

```sh
chmod +x psoc-linux-amd64
./psoc-linux-amd64 serve --listen 127.0.0.1:8080
```

For 64-bit ARM, use `psoc-linux-arm64` instead. The binaries are built with `CGO_ENABLED=0`, so they run directly on Alpine without installing Go or libc compatibility packages.

## Build a binary locally

If you do want to compile locally:

```sh
make build
./bin/psoc serve --listen 127.0.0.1:8080
```

Or build both Linux distribution binaries:

```sh
make binaries
```

This creates:

```text
dist/psoc-linux-amd64
dist/psoc-linux-arm64
```

## Docker

Docker does **not** compile the Go source. The Dockerfile only packages an existing `bin/psoc` executable into Alpine.

Build the binary first, then package it:

```sh
make docker
```

Equivalent commands:

```sh
make build
docker build -t psoc .
```

Run it:

```sh
docker run --rm \
  -p 127.0.0.1:8080:8080 \
  -v psoc-data:/data \
  psoc
```

If you downloaded a prebuilt artifact instead of compiling locally, place the binary matching the Docker target architecture at `bin/psoc` before `docker build`:

```sh
mkdir -p bin
cp psoc-linux-amd64 bin/psoc
chmod +x bin/psoc
docker build -t psoc .
```

### Docker Compose

Because the Dockerfile packages `bin/psoc`, prepare the binary first:

```sh
make build
docker compose up --build -d
```

The example publishes only to `127.0.0.1:8080` on the host.

If you intentionally expose the dashboard on a wider network, set `PSOC_API_TOKEN` and restrict network access.

## Dashboard

Start `psoc serve` and open `http://127.0.0.1:8080`.

The dashboard can:

- fetch and scan the bundled public proxy lists;
- show whether each source feed is reachable;
- show source fetch latency and imported target count;
- independently test each imported HTTP/SOCKS proxy;
- scan manually entered `host:port` targets.

A source being reachable does not mean its listed proxies are working. `psoc` verifies the proxies itself.

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
./bin/psoc scan \
  --input targets.txt \
  --output results.json \
  --format json \
  --protocols http,socks5,socks4 \
  --concurrency 64 \
  --timeout 8s
```

Use stdin/stdout with `-`:

```sh
printf '%s\n' '203.0.113.10:8080' | ./bin/psoc scan --input - --output - --format text
```

## Verification behavior

By default, each candidate proxy is asked to fetch `https://example.com/`.

- HTTP candidates use `CONNECT` for HTTPS verification.
- SOCKS5 uses the no-auth CONNECT handshake.
- SOCKS4 uses SOCKS4 or SOCKS4a depending on the destination host.
- HTTP status `200-399` is considered alive.

Override the destination with `--verify-url` / `PSOC_VERIFY_URL`. For repeatable or high-volume use, point this at an HTTP(S) endpoint you control.

## Safety boundaries

The checker scans imported or explicitly supplied proxy endpoints. It has no CIDR expansion, subnet sweep, port-range scan, or Internet-discovery mode.

By default it blocks loopback, RFC1918/private, link-local, multicast, and other non-public target addresses. `--allow-private` exists only for testing infrastructure you control.

Default limits include:

- concurrency: 64, hard-capped at 256
- timeout: 8 seconds, hard-capped at 60 seconds
- targets per scan: 5,000, hard-capped at 50,000
- bundled-source response size and fetch concurrency limits

Public proxies are untrusted infrastructure. Do not send credentials, cookies, API keys, tokens, personal data, or other secrets through them.

## Server configuration

| Variable | Default | Purpose |
|---|---:|---|
| `PSOC_LISTEN` | `127.0.0.1:8080` | dashboard/API bind address |
| `PSOC_DATA` | `./data/results.json` | persisted results |
| `PSOC_CONCURRENCY` | `64` | concurrent checks |
| `PSOC_TIMEOUT` | `8s` | per-candidate timeout |
| `PSOC_VERIFY_URL` | `https://example.com/` | URL fetched through the proxy |
| `PSOC_MAX_TARGETS` | `5000` | max targets per scan |
| `PSOC_ALLOW_PRIVATE` | `false` | permit private/local target IPs |
| `PSOC_API_TOKEN` | empty | bearer token required by write APIs |
| `PSOC_TARGETS_FILE` | empty | optional file to scan once at startup |

## API

- `GET /healthz`
- `GET /api/stats`
- `GET /api/proxies`
- `GET /api/status`
- `GET /api/sources`
- `POST /api/scan`
- `POST /api/sources/refresh`

Example manual scan request:

```sh
curl -X POST http://127.0.0.1:8080/api/scan \
  -H 'Content-Type: application/json' \
  -d '{"targets":"203.0.113.10:8080\nsocks5://203.0.113.11:1080","protocols":["http","socks5"]}'
```

With `PSOC_API_TOKEN` set, add `Authorization: Bearer <token>`.

## GitHub Actions

- `CI` runs tests/vet, builds the amd64 Docker input binary, validates the binary and Docker image, and uploads static Linux amd64/arm64 binaries as downloadable artifacts.
- `Manual proxy check` builds one static scanner executable, runs that binary, and uploads the scan results. Its default mode fetches the bundled public proxy sources; `manual-targets` checks only addresses entered for the run.

There is deliberately no scheduled Internet-wide scanning workflow.
