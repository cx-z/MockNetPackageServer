# MockNetPack Server

MockNetPack's central service manages device pairing, capture sessions, HTTP traffic, device-scoped mock rules, share links, accounts, and permissions. The iOS and Android SDKs capture traffic and execute mocks inside the app; the server provides the Web console, HTTP API, CLI, and remote MCP access.

This repository is based on [getmockd/mockd](https://github.com/getmockd/mockd) at `45c2906b095a0428842faaa931afab0edbde3fdb` (v0.7.1). Its Go module path remains `github.com/getmockd/mockd`. This README describes the current MockNetPack fork.

## Platform workflow

1. Integrate the SDK into the host app's debug build and initialize it early during startup.
2. Sign in to the Web console, select the app, and generate a pairing QR code. Scan it from the app's debug screen.
3. The SDK registers or reuses the device with the pairing token, saves the server address, and reconnects on subsequent launches.
4. Open the device's request viewer and activate a capture session. The SDK uses heartbeat responses to start capture and synchronize rules.
5. Create rules from captured traffic or enter them manually, edit the response, and enable the rule. Matching requests receive a local mock response; unmatched requests reach the real business server.

SDK rules match **HTTP method + URL path**, ignoring the host, query string, and request body. Rules are isolated by `(App, did)`. Capture and mock execution depend on the capture session state. Active viewers renew observer leases; when the last observer releases its lease or expires, the session ends and device rules are disabled. Ended-session traffic is kept according to the server's retention policy.

## Deployment addresses

`server_address` is a placeholder for the deployed server's DNS name or IP address. Replace it with an address reachable by phones, browsers, CLI clients, and MCP clients. The local project checkout is used for development and testing; the deployed service is the shared platform endpoint.

The examples use the default service ports. If your deployment exposes HTTPS or different ports through a reverse proxy, use its actual external URLs.

| Service | Example endpoint | Purpose |
|---|---|---|
| Web console | `http://server_address:4290/mocknetpack/` | Sign-in, pairing, devices, traffic, rules, and shares |
| Platform API | `http://server_address:4290/api/v1` | Capture management API used by SDKs and platform clients |
| Mock engine | `http://server_address:4280` | Separate mock data plane with HTTP, gRPC, WebSocket, and SSE support |
| Remote MCP gateway | `http://server_address:4291/mcp` | Streamable HTTP endpoint provided by `mocknetpack-mcp` |

Pairing QR codes must contain the externally reachable Platform API base URL, such as `http://server_address:4290/api/v1`. Verify the address and port are accessible from the device before pairing.

## Deploy from source

Go **1.26.9 or later** is required; CI uses Go 1.26.9. The scripts support macOS and Linux and use `ss` or `lsof` for port inspection. Run the following commands on the deployment server:

```bash
git clone https://github.com/cx-z/MockNetPackageServer.git
cd MockNetPackageServer

# Enter the actual Bundle IDs / applicationIds, one per line.
# This Git-ignored catalog is reloaded without restarting the service.
cat > .allowed-apps.local <<'APPS'
com.example.iosapp
com.example.androidapp
APPS

./start.sh
```

`start.sh` builds `bin/mockd-bin` and `bin/mocknetpack-mcp-bin` when source files change, restarts the service and MCP gateway, and persists runtime data in `mockd-data/`. Use `./start.sh rebuild` to force rebuilding. The script stops old processes occupying its fixed service ports; run it on the dedicated deployment host.

Open `http://server_address:4290/mocknetpack/` in a browser. Public registration creates a developer account, which can manage its own devices and generate pairing QR codes. The phone app's Bundle ID / applicationId must match the selected app. Pairing tokens expire after 10 minutes by default; refresh the QR code when it expires.

To create the first administrator, start the service in the foreground on the deployment server and replace the password placeholder:

```bash
mkdir -p bin
go build -o bin/mockd-bin ./cmd/mockd
./bin/mockd-bin start --port 4280 --admin-port 4290 \
  --data-dir ./mockd-data \
  --create-admin admin --admin-password '<your-password>'
```

Only `--create-admin` creates administrator accounts. Creating an existing username fails. Remove the two account-creation flags on later starts. Stop the foreground process with Ctrl+C before switching to `./start.sh`.

Runtime files on the deployment host:

| Path | Contents |
|---|---|
| `mockd-data/` | Persistent devices, accounts, sessions, rules, and traffic |
| `.allowed-apps.local` | App catalog; alternatively configure `MOCKD_ALLOWED_APPS_FILE` or comma-separated `MOCKD_ALLOWED_APPS` |
| `~/.mockd/daemon.log` | Main service log; the startup script archives the previous log before restarting |
| `~/.mockd/mcp.log` | MCP gateway log |
| `/tmp/mockd-web.pid`, `/tmp/mocknetpack-mcp.pid` | Process records used by the startup script |

`./stop.sh` currently stops only the main service listening on ports 4280 / 4290. Stop the MCP gateway separately. Web assets are read from `web/mocknetpack/` relative to the working directory. When starting the binary elsewhere, point `--web-dir` to that directory. Deploy the Web assets together with the binary.

## Authentication and API

Web management endpoints accept a sign-in session or an account's long-lived API key. Create a key in the Web console's account / API Keys panel; its plaintext is returned only once. CLI and MCP requests use `Authorization: Bearer <key>` and inherit the account's role and device ownership permissions.

SDK registration, heartbeat, upload, and incremental rule synchronization use a separate channel. Apps do not need an embedded Web account or API key. The initial pairing token establishes device ownership; registered devices can reconnect. `--no-auth` is for local testing; the normal startup script enables authentication. Native mockd admin API keys and platform `mnpk_` account keys belong to separate channels.

The following routes are prefixed with `/api/v1`:

| Feature | Example routes |
|---|---|
| Sign-in and account keys | `POST /auth/login`, `POST /auth/keys` |
| App catalog and pairing | `GET /apps`, `POST /pairing-tokens` |
| SDK registration and heartbeat | `POST /devices/register`, `POST /devices/{app}/{did}/heartbeat` |
| Devices and rules | `GET /devices`, `/devices/{app}/{did}/mock-rules` |
| Sessions and observers | `POST /sessions`, `POST /sessions/{id}/viewers` |
| Traffic upload and queries | `POST /traffic`, `GET /sessions/{id}/traffic`, `GET /traffic/{id}` |
| Share snapshots | `POST /shares`, `GET /shares/{id}` |

See [openapi/mocknetpack.yaml](openapi/mocknetpack.yaml) for the API contract. Current routes and implementations define parameter and permission behavior.

## CLI and remote MCP

The platform CLI is a separate executable from native `mockd`. Build it from this repository, then connect it to the deployed service:

```bash
go build -o bin/mocknetpack ./cmd/mocknetpack
./bin/mocknetpack --help

# Create the key in the Web console. Use the admin origin without /api/v1.
export MOCKNETPACK_SERVER='http://server_address:4290'
export MOCKNETPACK_API_KEY='<your-api-key>'
./bin/mocknetpack --server "$MOCKNETPACK_SERVER" devices list
./bin/mocknetpack --server "$MOCKNETPACK_SERVER" traffic list --app com.example.iosapp --did '<device-id>'
./bin/mocknetpack --server "$MOCKNETPACK_SERVER" traffic export --app com.example.iosapp --did '<device-id>' -o traffic.json
```

Pass the CLI server URL explicitly with `--server`; the key can be read from `MOCKNETPACK_API_KEY`. Commands cover device listing, traffic queries/export, rule creation/update/toggling, and shares. stdout contains JSON; logs and errors go to stderr. Exit codes are 0 (success), 1 (business error), and 2 (usage error).

`start.sh` starts the MCP gateway on the deployment host. To run it separately:

```bash
go build -o bin/mocknetpack-mcp-bin ./cmd/mocknetpack-mcp
MOCKNETPACK_SERVER=http://server_address:4290 \
  ./bin/mocknetpack-mcp-bin --http-addr :4291
```

Configure a Streamable HTTP MCP client with URL `http://server_address:4291/mcp` and header `Authorization: Bearer <your-api-key>`. The gateway forwards each caller's key to the platform for permission checks.

The gateway currently provides 16 tools covering capture status/start/observer renewal/release, device listing, device traffic and individual traffic details, rule creation/editing/querying/deletion/toggling, and share creation/retrieval. Keep the same `observerId` throughout a capture workflow, renew according to the returned lease TTL, and call `release_capture_observer` when finished.

## Retained capabilities and compatibility

The native engine retains HTTP/HTTPS, gRPC, WebSocket, SSE/chunked streaming, stateful tables, Chaos, TLS/mTLS, audit logging, and OpenAPI validation. These engine capabilities are separate from SDK-side HTTP capture and mock execution.

This fork has removed MQTT, GraphQL, SOAP, OAuth simulation, server-side HTTP Proxy/recording conversion, Tunnel, native `mockd mcp`, and `mockd init/import/export`. Upstream commands, installation scripts, and images for those features do not describe this platform. Platform `mocknetpack traffic export` and remote MCP remain available.

## SDK repositories

- [iOS: MockNetPackKit](https://github.com/cx-z/MockNetPackKit) — URLProtocol / URLSession integration through CocoaPods or Swift Package Manager.
- [Android: MockNetPackAndroid](https://github.com/cx-z/MockNetPackAndroid) — JitPack integration with an OkHttp application interceptor.

See each SDK's README for integration, permissions, and Release isolation.

## Local development and testing

Use the local checkout to develop and test changes. The deployment commands above are run on the server host; client examples point to `server_address`.

```bash
go build ./...
go test ./...
golangci-lint run --timeout=5m
govulncheck ./...

# Same parameters as the CI Test job.
go test -race -coverprofile=coverage.out -covermode=atomic ./...
```

Lint uses golangci-lint v2.11.2. Some E2E tests require Docker through testcontainers. Tests that start services also require available ports. Repository rules are in [AGENTS.md](AGENTS.md).

## Attribution and license

The upstream [Apache License 2.0](LICENSE) and [NOTICE](NOTICE) are retained. MockNetPack-specific functionality and fork behavior are defined by this repository, rather than upstream product comparisons, cloud-service plans, or protocol-count claims.
