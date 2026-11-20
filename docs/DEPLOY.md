# Sovereign Deploy (simplest path)

Prebuilt `sovereign-server` binaries for Linux and Windows are published on every
`v*` tag via `.github/workflows/release.yml` (frontend already embedded —
single file, no Node.js needed at runtime).

## Option 0 — from a source checkout: one command

The deploy script takes the working tree to a running, verified server:

```
preflight -> build (frontend bundle + Go binary) -> stop the old instance
-> start the new one -> health/ready/login smoke -> rollback
```

```bash
bash scripts/deploy.sh                 # Linux / macOS
powershell -File scripts/deploy.ps1    # Windows
```

What it adds over "build and restart by hand":

- **Preflight** — the toolchain and the config must exist; the probe port is
  read from `server.port`; a wildcard bind is called out so the operator
  knows the console is reachable from other hosts (same-origin access needs
  no configuration; `server.allowed_origins` is only for separately hosted
  consoles).
- **One build path** — `scripts/build-embedded.ps1` owns the
  frontend → `internal/webdist/dist` sync (staged copy, LF-normalized, so the
  committed bundle stays byte-identical on every platform). The deploy script
  never copies the bundle itself.
- **A clean handover** — the old instance is stopped and the port is waited
  out, so the new one never fights for the bind.
- **Verification** — `/health`, `/ready` and `/login` are probed; a failed
  `/health` is not silently ignored.
- **Rollback** — the previous binary is kept as `sovereign-server.prev` (or
  `sovereign-server.exe.prev`). A new instance that does not come up healthy
  is rolled back to it and started again: a bad build never leaves the box
  down.

Useful flags: `--skip-checks` (skip the frontend gates), `--skip-frontend`
(backend-only change), `--no-start` (build only), `--no-rollback`,
`--port`, `--timeout`.

## Prebuilt downloads — one-click start

Every `v*` tag publishes two kinds of asset:

| Asset | Use |
| --- | --- |
| `sovereign-v<version>-windows-<arch>.zip` | unzip, double-click `start.cmd` |
| `sovereign-v<version>-linux-<arch>.tar.gz` | `tar xzf … && cd … && ./start.sh` |
| `sovereign-server-<os>-<arch>[.exe]` | the raw binary, for Docker images and your own packaging |

The archives carry the embedded-console binary (renamed to `sovereign-server` /
`sovereign-server.exe`), `config.example.yaml`, `LICENSE`, `QUICKSTART.txt` and
the launcher. The launcher creates `config.yaml` on first run, prints the
console URL, and the server prints the generated `admin` password in the same
window. Nothing else has to be installed: no Node.js, no Go, no database
server.

Both the raw binaries and the archives ship a `.sha256` plus a detached
Ed25519 `.sig` over that checksum:

```bash
sha256sum -c sovereign-v2.7.0-linux-amd64.tar.gz.sha256
# Detached Ed25519 signature over that checksum file (no secret needed):
go run ./cmd/sign-release -verify \
  -key 2583e905edf1b67be9caaf0f7fe4efc402b8456c68fd458a4fb21a9083318d52 \
  sovereign-v2.7.0-linux-amd64.tar.gz.sha256
```

Release signing public key (set it as `crypto.update_signing_key` to require
signatures on hot updates; `crypto.require_release_signature: true` makes an
unverifiable update fail closed):

```
2583e905edf1b67be9caaf0f7fe4efc402b8456c68fd458a4fb21a9083318d52
```

## Option A — Linux, Docker (recommended, SQLite, zero-config)

The published image is a **multi-arch manifest** for `linux/amd64` and `linux/arm64`,
so the same `docker pull` works on an Intel/AMD host and on Graviton, Ampere or a
Raspberry Pi. A release publishes it as `vX.Y.Z`, `X.Y.Z` and `:latest`; `edge` is
the rolling development build.

```bash
# a release (recommended)
docker pull ghcr.io/r0si/sovereign:2.7.2

# what does the tag actually contain?
docker buildx imagetools inspect ghcr.io/r0si/sovereign:2.7.2

# pin for a reproducible deployment
docker pull ghcr.io/r0si/sovereign@sha256:<digest>
```

```bash
cp config.example.yaml config.yaml
docker compose up -d --build
curl -k https://127.0.0.1:8000/health   # {"status":"ok",...}
```

Secrets (JWT, beacon key, TLS cert) auto-generate on first run. Missing storage
keys (`crypto.loot_key` / `extc2_key` / `backup_key` / `credential_key` / `csrf_key`)
are generated at startup and written back to `config.yaml`, so the example
config is a working first start; set them explicitly (or via `FORGEC2_*` env)
when the config file is mounted read-only. Data persists in the
`sovereign_data` volume (`FORGEC2_DATA_DIR=/data`, which also anchors logs and
backups). No `.env` needed unless you want overrides (see `.env.example`).
Postgres is opt-in:
`docker compose --profile postgres up -d --build` (set `DB_PASSWORD`,
`FORGEC2_DB_DRIVER=postgres`, `FORGEC2_DB_DSN`).

> **Postgres backup caveat:** the built-in database backup/restore/VACUUM
> endpoints are SQLite-only (they use `VACUUM INTO` plus file replacement).
> On a Postgres deployment they return `501` with this explanation instead of
> pretending to work — schedule `pg_dump`/`pgBackRest` yourself.

### Backups

Encrypted snapshots (`<data_dir>/backups/*.fbk`, AES-256-GCM) run on a schedule:

```yaml
backup:
  retain: 14          # snapshots kept (0 = default 7)
  schedule: "daily"   # hourly | daily | weekly | monthly
```

Env equivalents: `FORGEC2_BACKUP_RETAIN`, `FORGEC2_BACKUP_SCHEDULE`.

Freshness is observable, so a stalled job is not discovered at restore time:

- `GET /api/v1/health` → `backup: { retain, age_seconds, last_success, last_failure, last_error }`
- Prometheus: `sovereign_backup_age_seconds`, `sovereign_backup_success_total`, `sovereign_backup_failures_total`

Alert on `sovereign_backup_age_seconds` exceeding ~2× your schedule interval.

### Production overlay

`docker-compose.prod.yml` hardens the base stack for real deployments:
`read_only` rootfs (+ tmpfs for `/tmp` and home), `mem_limit` / `pids_limit`,
`FORGEC2_JWT_SECRET` required, storage keys forwarded from `.env`, bind
`0.0.0.0` by default.

```bash
cp config.example.yaml config.yaml
cp .env.example .env            # then set FORGEC2_JWT_SECRET=$(openssl rand -hex 32)
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build
curl -fsS https://127.0.0.1:8000/health
```

## Option B — Linux, bare binary

```bash
# from the GitHub Release page for your version:
curl -LO https://github.com/r0si/sovereign/releases/download/vX.Y.Z/sovereign-server-linux-amd64
chmod +x sovereign-server-linux-amd64
cp config.example.yaml config.yaml
./sovereign-server-linux-amd64 -config config.yaml
```

## Option C — Windows, bare binary

```powershell
# download sovereign-server-windows-amd64.exe from the GitHub Release page,
# rename it, then from that folder:
Copy-Item config.example.yaml config.yaml
.\sovereign-server.exe -config config.yaml
```

Open `https://<host>:8000` (self-signed cert on first run; TLS on by default).
Data (SQLite + uploads) lives under `data/` (or the `sovereign_data` volume).

The console binds **every interface** by default (`server.host: 0.0.0.0`), so
it can be operated from another host or container network. Two consequences:

- Opening this server's own address from any browser works immediately: a
  same-origin request needs no `server.allowed_origins` entry. List a hostname
  under `server.allowed_origins` only when a console hosted on a **different**
  origin must call this API. A loopback page (Vite dev server) may reach a
  loopback server across ports; a loopback page cannot reach a public host.
- Set `server.host: 127.0.0.1` to keep the console local. The SOCKS relay
  stays local either way (`server.socks_listen_host`).

TLS posture is configurable per termination point: extra `https`/`tls`
listeners can require verified client certificates, verify them against their
own CA, or raise their floor to TLS 1.3 — see
[`docs/LISTENER_TLS.md`](LISTENER_TLS.md) for the merge rules and the
client-certificate path for implants.

## Hot update (from the console)

The console can replace the running server with a newer release. It is opt-in
because it phones home to `api.github.com`:

```yaml
# config.yaml
server:
  update_check_enabled: true
  update_check_repo: r0si/sovereign   # defaults to the repository the binary was built from
```

With the check enabled, Settings → *Check for updates* (or the banner) offers
the new release, and an administrator can install it from the dialog. The
update:

1. picks the asset that matches the running platform
   (`sovereign-server-<os>-<arch>[.exe]`) — a release that carries no binary for
   this platform, or an ambiguous one, is refused;
2. downloads it into `<install dir>/.update/`, verifies the SHA-256 published
   next to it (and the detached Ed25519 signature when
   `crypto.update_signing_key` is set), and snapshots the SQLite database;
3. hands over to a generated restarter that waits for the old process to
   exit, installs the new binary and starts it **with the original command
   line and working directory**;
4. rolls back: if the new build does not report a healthy boot within 60
   seconds, the previous binary (and the database snapshot) is restored and
   the old build is started again.

The outcome is audited (`hot_update_result`) and readable at
`GET /api/update-progress` under `last_result`
(`success` / `failed` / `rolled_back` / `interrupted`).

#### The release asset contract

The updater reads the GitHub releases API and installs exactly one asset. A
release (including a fork's) must therefore publish, for the platform it
targets:

| Asset | Required | Purpose |
|---|---|---|
| `sovereign-server-<os>-<arch>[.exe]` | yes | the installable binary |
| `sovereign-server-<os>-<arch>[.exe].sha256` | yes | checksum of **that** binary |
| `sovereign-server-<os>-<arch>[.exe].sha256.sig` | when signing is on | detached Ed25519 signature |

Anything else in the release — the one-click archives, the SBOM, `QUICKSTART.txt`,
other platforms' binaries — is ignored by the picker, and a checksum that
belongs to a different platform is **not** accepted for this one. A release
that publishes no binary for the running platform, or two binaries that both
match it, is refused instead of guessed. `internal/server/update_release_contract_test.go`
pins this contract against a simulated multi-platform release.

### Manual upgrade (and the escape hatch)

The hot update needs a working installation; to replace the binary by hand:

```powershell
# Windows
$env:V = "v2.7.1"
curl.exe -L -o sovereign-server.exe.new "https://github.com/r0si/sovereign/releases/download/$env:V/sovereign-server-windows-amd64.exe"
# verify against the published checksum before swapping:
curl.exe -L -o server.sha256 "https://github.com/r0si/sovereign/releases/download/$env:V/sovereign-server-windows-amd64.exe.sha256"
Get-FileHash sovereign-server.exe.new -Algorithm SHA256   # compare with server.sha256
taskkill /f /im sovereign-server.exe
Move-Item -Force sovereign-server.exe sovereign-server.exe.prev
Move-Item -Force sovereign-server.exe.new sovereign-server.exe
.\sovereign-server.exe -config config.yaml
```

```bash
# Linux
V=v2.7.1
curl -LO "https://github.com/r0si/sovereign/releases/download/$V/sovereign-server-linux-amd64"
sha256sum -c "sovereign-server-linux-amd64.sha256" 2>/dev/null || curl -LO "https://github.com/r0si/sovereign/releases/download/$V/sovereign-server-linux-amd64.sha256"
sha256sum -c sovereign-server-linux-amd64.sha256
mv sovereign-server sovereign-server.prev && mv sovereign-server-linux-amd64 sovereign-server
./sovereign-server -config config.yaml
```

`scripts/deploy.ps1` / `scripts/deploy.sh` do the same thing from a source
checkout, with a health check and an automatic rollback to the `.prev` binary.

> **Upgrading from 2.7.0**: the hot update in 2.7.0 could not select the right
> asset and never restarted the server, so 2.7.0 cannot update itself. Update
> **once by hand** (above), and every later release can be installed from the
> console.

## Cut a release (maintainers)

```bash
git tag vX.Y.Z
git push origin vX.Y.Z   # release.yml builds linux/windows × amd64/arm64
```

The tag is stamped into the binary (`-version` reports it; `/health`
reports it too). The `linux/amd64` build asserts this in CI and fails the
release if the tag didn't make it in. Custom `FORGEC2_PORT` is honored by
both the server and the container healthcheck.

The same tag also publishes the **container**: the release workflow calls
`container.yml`, which builds a multi-arch image (`linux/amd64` **and**
`linux/arm64`) and tags it `vX.Y.Z`, `X.Y.Z` and `:latest`. The image build
takes about three minutes because the builder stages run on the build machine
and cross-compile the Go binary (`TARGETOS`/`TARGETARCH`) instead of emulating
the target architecture. The published binary is stripped (`-s -w`, 59.5 MiB)
and the release fails if it grows past 70 MiB.

## Beacon rate limits

Two budgets guard the beacon path, and they answer different questions:

| Budget | Keyed by | What it stops |
|---|---|---|
| address | the source address | an unauthenticated flood from one address |
| agent | the agent id, once the frame is decrypted | one misbehaving agent spending everyone else's allowance |

The **address budget scales with the number of agents the server has seen
from that address**. A cloud provider's NAT egress is one address for hundreds
of implants, and a fixed per-address budget throttles them all together — the
symptom is a fleet that "went offline" while the server was idle. An address
with no known agent keeps the base budget, so an unauthenticated flood is still
cut, and the scale factor is capped (64 agents per address) so one address
cannot inflate its own budget without bound.

`rate_limit.beacon.limit` / `.window` set both budgets. A refused check-in
answers **429 with `Retry-After`**; the implant treats that as retryable and
backs off instead of failing. `/ready` publishes `checks.beacon_agents`
("N agent(s) tracked, M check-in(s) refused") and warns while M is above zero;
`/api/v1/health` publishes `beacon_agents_tracked` and `beacon_agents_refused`.

```yaml
rate_limit:
  beacon:
    limit: 120      # per agent, and per address (scaled by the agents there)
    window: 60      # seconds
```

## Monitoring what the server says about itself

`GET /health` is liveness and `GET /ready` is readiness, but `/ready` also carries warnings a monitor should act on. Two of
them are about things that keep working while being useless, which is why they are listed explicitly:

| Check | Warning when | What to do |
|---|---|---|
| `notification_delivery` | a route has **never delivered** since the process started | send a test from the console; the response now says *why* it failed |
| `automation_actions` | a rule's actions have **never landed** | the rule-set health report names the rule and the last dispatch error |
| `plugins` | one or more packages were **rejected at load** | `GET /api/plugins/failures` names them and suggests the next step |
| `auto_retries` | a **burst** of read-only tasks was re-issued automatically (>20 since start) | the link is timing out, not the task: check the beacon's connectivity before the report's reasons |
| `audit_chain` | the chain does not verify in the ready window | treat as an incident: `docs/AUDIT_EXPORT.md` explains what it can and cannot prove |
| `task_queue` | a beacon holds a pending task older than **its own sleep interval plus two minutes** | the beacon is not polling, not slow: `GET /api/stats/tasks/queue` names the host and the age |
| `build_toolchain` | the server process cannot build a payload (`go` missing from its PATH, or an unwritable `GOCACHE`) | `GET /api/generate/toolchain` names the missing piece; a shell where `go` works proves nothing about the service account |
| `build_history` | the last 24 h of builds **all failed** (at least three of them) | `GET /api/stats/builds/failures` names the cause; one failed build is a typo, a run of them is a broken toolchain |
| `ws_slow_clients` | a console socket could not keep up with a **reliable** frame and was closed so it would resync | nothing is lost (the browser reconnects and refetches), but a rising count means browsers are falling behind: check client CPU/network before believing a page is stuck |

Both delivery checks use "never delivered", not "anything failed": one failure after a hundred successes is a blip, and a
probe that warns on blips gets muted. Neither makes the server **unready** - the process is fine, the notification path is
not - so `status` stays `ok` and the condition travels in `warnings`.

Alerting on `warnings` being non-empty is the intended integration; alerting on the status code alone will miss all three.

`GET /ready?download=1` files the snapshot instead of polling it: the same payload with
`Content-Disposition: attachment; filename="ready_<instant>.json"`, `X-Export-Scope: probe at <RFC3339>` and a `probed_at`
field. Use it when a probe result is going into a ticket — the instant appears in three places because a snapshot without one is
not evidence — and leave the plain `/ready` for the monitor, which is the cheaper request.

Two checks are the opposite: they answer **not ready** (503), because the process cannot do its job without them.

| Check | Not ready when | What to do |
|---|---|---|
| `database` | the handle is missing, or a probe query fails | the message distinguishes *unavailable* (never initialised) from *error* (the probe failed) |
| `listeners` | the listener registry cannot be read | usually the same cause as `database`; a listener count of `0` is **ready**, because a console with no C2 listener running still works |

One check is neither: `bundle_signing` reports whether handover bundles will be signed (`FORGEC2_BUNDLE_SIGNING_KEY`
configured) and **never** warns, because an unsigned bundle is a legitimate choice and a probe that warns about one is a
probe that gets muted. It exists so an operator can see which artefact this deployment produces without reading the startup
log.

The distinction is the point of this section: `database` and `listeners` are **liveness of function**, while the warnings
above are **health of features**. A monitor that restarts on a warning restarts a working server; one that ignores the first
two reports a broken one as healthy.

Each warning is **also written to the audit log once per episode** - `observability_warning` when it starts (or its count
rises) and `observability_recovered` when it returns to zero - so the trail records *when* a route started looking broken
and *when* it was fixed. A row per poll would bury the trail in its own alarm, which is why the writes are tied to
transitions rather than to the probe.

A counter that has been fixed can be cleared, which is what removes the warning: `POST /api/notification-routes/stats/reset`
and `POST /api/automation/action-stats/reset` (both audited), discoverable at runtime in `/api/exports/capabilities`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `port 0.0.0.0:8000 is already in use` | Another instance is running: `taskkill /f /im sovereign-server.exe` (Windows) or `docker compose down` / `pkill sovereign-server` (Linux). Or change `server.port` in `config.yaml`. |
| `unable to open database file` (Docker) | `/data` not writable — fixed in-image (nonroot-owned seed); `docker compose down -v` on a stale root-owned volume, then `up` again. Bare binary: run from a writable directory. |
| `unhealthy` container right after `up` | Normal for ~30s (Go build + first-run migrations). `docker compose ps`; `start_period` covers it. If still unhealthy: `docker compose logs sovereign`. |
| Browser cert warning | Expected: self-signed cert generated at `data/server.crt`. Accept once, or put `cert_file`/`key_file` with a real cert in `config.yaml`. |
| Windows SmartScreen / Defender flag | Unsigned exe — click “More info → Run anyway”; consider adding a Defender exclusion for the folder on lab machines. |
| Windows Firewall prompt on first start | Allow private networks so agents can reach port 8000. |
| `AI not configured` | Set `ai.api_key` in `config.yaml` or `FORGEC2_AI_API_KEY` env. |
| Agents can't check in (Docker) | `FORGEC2_HOST` defaults to `127.0.0.1` — set it to the server's LAN IP (or `0.0.0.0` listen + agent URL pointing at you) and open the port. |
| `DB_PASSWORD ... required` | Only when using `--profile postgres`. Plain `docker compose up` (SQLite) doesn't need `.env` at all. |
