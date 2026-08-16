# Public proxy sources

`psoc` includes a curated catalog of public proxy-list feeds for HTTP, SOCKS4, and SOCKS5. The dashboard does **not** trust a provider's "working" label: it first checks whether each list feed is reachable, parses and de-duplicates the entries, and then verifies every imported proxy using the configured `--verify-url`.

## Bundled feeds

| Provider | Protocols | Feed |
| --- | --- | --- |
| ProxyScrape | mixed HTTP/SOCKS4/SOCKS5 | `https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&proxy_format=protocolipport&format=text` |
| Proxifly | HTTP | `https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/http/data.txt` |
| Proxifly | SOCKS4 | `https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks4/data.txt` |
| Proxifly | SOCKS5 | `https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt` |
| monosans/proxy-list | HTTP | `https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt` |
| monosans/proxy-list | SOCKS4 | `https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks4.txt` |
| monosans/proxy-list | SOCKS5 | `https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt` |
| TheSpeedX/SOCKS-List | HTTP | `https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt` |
| TheSpeedX/SOCKS-List | SOCKS4 | `https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks4.txt` |
| TheSpeedX/SOCKS-List | SOCKS5 | `https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks5.txt` |
| roosterkid/openproxylist | HTTP/HTTPS proxy | `https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt` |
| roosterkid/openproxylist | SOCKS4 | `https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS4_RAW.txt` |
| roosterkid/openproxylist | SOCKS5 | `https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS5_RAW.txt` |

The catalog is intentionally code-reviewed rather than accepting arbitrary source URLs through the dashboard. That avoids turning the server into a generic server-side URL fetcher.

## Dashboard

Open the dashboard and select **Fetch + scan public lists**.

The **Public proxy sources** table shows two different things:

- **Availability**: whether the list URL itself returned a usable proxy list.
- **Results**: whether each imported proxy actually worked from the machine running `psoc`.

API endpoints:

```text
GET  /api/sources
POST /api/sources/refresh
```

`POST /api/sources/refresh` uses the same bearer token protection as `POST /api/scan` when `PSOC_API_TOKEN` is configured.

## GitHub Actions

Run **Manual proxy check** and keep `mode` set to `public-sources`.

The workflow:

1. starts the local `psoc` dashboard/API;
2. fetches and checks every bundled list feed;
3. de-duplicates imported protocol/address pairs;
4. checks up to 5,000 proxies with bounded concurrency and timeout;
5. uploads `results.json`, `sources.json`, and `server.log` as the `psoc-results` artifact.

Use `manual-targets` mode when you want to check only addresses you supply yourself.

## Security note

Public proxies are untrusted infrastructure. Do not send credentials, session cookies, API keys, personal data, or other secrets through them. Prefer HTTPS verification targets and treat a successful connectivity check as reachability only, not as evidence that a proxy is private, safe, anonymous, or non-malicious.
