# Capacity and hot-path performance

How many implants one teamserver core can carry, how that was measured, and how
to re-measure it on another machine. Everything here is read-only or opt-in:
the profiling endpoint is off by default, and none of it changes what the
server serves.

This document exists because "it feels fast" is not a capacity plan. The
numbers below are from one machine, so treat them as ratios and as a
regression baseline, not as a promise about yours.

## 1. The beacon check-in is the hot path

Every implant walks the same path every few seconds to every few minutes,
depending on its sleep interval: encrypted frame in, per-agent key derivation,
agent upsert, pending-task claim, encrypted frame out. Operator traffic is
bursty; beacon traffic is the steady-state load that sizes the box.

Three benchmarks in `internal/server/beacon_hotpath_bench_test.go` cover its
shapes, all through the real handler and router over an in-memory SQLite
database:

| Benchmark | Shape |
|---|---|
| `BenchmarkBeaconCheckin` | Idle check-in: no tasks waiting, nothing to report. The steady state of a quiet fleet. |
| `BenchmarkBeaconTaskDelivery` | Check-in that claims one pending task (queue selection, claim, task in the response). |
| `BenchmarkBeaconCheckinWithResult` | Check-in carrying one shell result (status transition, output storage, the audit/event rows it drives). |

Each iteration includes the request and response crypto (AEAD with the
per-agent session key), because dropping it would hide the cost that dominates
a small payload.

## 2. Measured numbers

Reference machine: Intel Core Ultra 5 225H, Windows 11, Go 1.26.0, 2026-10.
Command:

```bash
go test ./internal/server/ -run '^$' -bench 'BenchmarkBeacon' -benchtime=2s -count=2
```

Two runs per benchmark (run-to-run spread was about ±7%):

| Benchmark | ns/op | check-ins/s per core | allocs/op | B/op |
|---|---|---|---|---|
| `BenchmarkBeaconCheckin` | 535k – 551k | ≈ 1 840 | 1 257 | 158 KB |
| `BenchmarkBeaconTaskDelivery` | 716k – 735k | ≈ 1 380 | 1 500 | 206 KB |
| `BenchmarkBeaconCheckinWithResult` | 939k – 1 048k | ≈ 1 010 | 2 104 | 257 KB |

### Registration-key recovery (memoized, 2026-11-19)

`BenchmarkRegKeyDerivation` in `internal/server/regkey_bench_test.go` isolates
the per-call cost of recovering an implant's v3 registration key. The uncached
path reads the `reg_secrets` row and AEAD-unseals it; that runs on the beacon
path (kill-switch enforcement) and once per chunk of a file-chain transfer.
`regSecretByID` now memoizes by secret id (bounded FIFO, 256 entries, invalidated
on beacon-key rotation), and kill-switch enforcement reuses the loaded implant's
`SecretID` to skip the extra `Pluck`.

```bash
go test ./internal/server/ -run '^$' -bench BenchmarkRegKeyDerivation -benchtime=500x -benchmem
```

| Variant | ns/op | allocs/op | B/op |
|---|---|---|---|
| `raw-unseal` (uncached DB read + unseal) | 13 112 | 83 | 5 994 |
| `memo` (bounded secret-id memo) | 30 | 1 | 32 |

~437× faster and ~2 700× fewer allocations on the repeated path. The memo is
invalidated in both store-rebuild sites (`server.go`, `config_sync.go`) so a
rotated beacon key can never serve a key unsealed under the previous master.
Re-measure before trusting these ratios on different storage; the DB read is an
in-memory SQLite lookup here.

### Dedup eviction is FIFO, not a full-map scan (2026-11-22)

The beacon and result dedup caches are capped at `MaxBeaconDedupEntries`
(10 000) for a `BeaconDedupWindow` of 5 s. A fleet above 2 000 beacons/s
produces more distinct frames than the cap in every window, so **every**
check-in has to evict. The old implementation found the oldest entry by
scanning the whole map; the new one keeps an insertion FIFO and pops from the
head, with the queue's stale entries skipped when a key was re-inserted.

```bash
go test ./internal/server/ -run '^$' -bench 'Benchmark(Beacon|Result)DedupAtCapacity' -benchtime=2s -benchmem
```

| Benchmark (cache at cap, filled through the public path) | before | after |
|---|---|---|
| `isDuplicateBeacon` | 170 363 ns/op | 266–369 ns/op (2 allocs) |
| `isDuplicateResult` | 214 278 ns/op | 288–329 ns/op (2 allocs) |

The queue is bounded by the same cap (compacted as the head advances), and an
insert trims to `cap-1` first so the frame being processed is never the
eviction victim. When the queue is exhausted but the map is still over the
limit — entries inserted outside the API, e.g. a fixture or a restored
snapshot — eviction falls back to the old oldest-first scan for that one call,
which is what keeps `TestDedupEvictsOldestFirst`'s contract intact.
`TestBeaconDedupEvictsOldestFirst`, `TestEvictDedupOldestSkipsReinsertedKey`,
`TestEvictDedupOldestFallsBackWhenQueueEmpty` and
`TestResultDedupEvictsOldestFirst` pin those properties.

### Relay-parent dispatch reads ids, not rows (2026-11-22)

`fetchRelayedChildTasks` ran on every beacon of every agent and loaded full
`db.Implant` rows (100+ columns) only to collect child ids. It now `Pluck`s the
ids, and its two child-task queries select the same dispatch columns as
`fetchPendingTasks` (result/error blobs never reach the wire and their
`AfterFind` decrypt is wasted).

The same round pre-sizes the slices the four hot claim queries scan into
(`make([]db.Task, 0, 4)`). GORM allocates a 20-element slice whenever the
destination has zero capacity, and `db.Task` is wide enough that the empty
claim query alone cost ~28 KB — measured, not assumed.

`BenchmarkBeaconCheckin` (idle check-in, which still pays for the child lookup
and both claim queries):

| | before | after |
|---|---|---|
| B/op | 165.9 KB | 101.5 KB |
| allocs/op | 1 267 | 1 216 |

Two runs each: 648/773 µs → 559/663 µs. The latency difference is inside this
machine's run-to-run spread, so the deterministic claim is the allocation
reduction (~39%), not the wall-clock number. `TestFetchPendingTasksRespectsCapacity`
covers the case where more rows arrive than the pre-sized capacity, and the
never-cross-claims/dedupe/recarry tests keep the claim semantics pinned.

### The beacon response reuses the accepted sequence (2026-11-23)

`processBeaconWithBudget` called `currentLastSeq` for the response's `last_seq`
— a second read of the value `acceptSeq` had just persisted with its
compare-and-swap. A frame only reaches that code after the swap succeeded, so
`req.Seq` *is* the stored value; the resync path still reads the row because it
answers rejected frames that never touched it.

`BenchmarkBeaconCheckin` (idle check-in): **101.5 KB → 97.0 KB** and **1 216 →
1 157 allocations** per request (one fewer query). Latency runs on this host
swing too widely to attribute the difference; the allocation count is
deterministic.

### Page payloads are snake_case-only (2026-11-22)

`renderPageOrJSON` used to add a snake_case alias **next to** every PascalCase
key, so any collection in a page response serialized twice. The task list (50
tasks + the 500-agent picker) shipped **214,945 bytes** per request; half of it
was duplicated arrays. The payload now carries one key per field (renamed in
place, snake_case value wins on a collision), and the whole server-side i18n
dictionary (`locales_json`, ~12.6 KB) is no longer attached to every page — the
React console has its own i18n and never read it.

`BenchmarkTaskList` reports `response_bytes` for exactly this reason:

| | before | after |
|---|---|---|
| `response_bytes` | 214 945 | 101 344 |
| B/op | 1.47 MB | 1.06 MB |
| allocs/op | 8 639 | 7 392 |
| ns/op (tenant-scoped / unscoped) | 3.12 / 4.42 ms | 2.26 / 2.25 ms |

The console reads the snake_case keys (three legacy PascalCase-only reads were
updated), and `TestListenerDetailCountKeepsTenantScope` / the agent-detail stats
test decode the new shape. The wire change is one key per field, not a different
document: every value keeps its type and position.

### The task page's agent picker sends three fields (2026-11-23)

`handleTaskHistory` selected only `id, hostname, ip` but scanned the rows into
`db.Implant`, and the JSON encoder serializes **every** field of the model — so
each of the 100 picker entries carried dozens of zero-valued columns that the
console never reads. A three-field `taskPageAgent` struct keeps the same JSON
keys and cuts the page again:

| (`BenchmarkTaskList`) | after snake_case-only | after the picker struct |
|---|---|---|
| `response_bytes` | 101 340 | 28 740 |
| B/op | 1.06 MB | 528 KB |
| allocs/op | 7 392 | 6 982 |

The frontend is untouched: the picker reads `id`/`hostname`/`ip` exactly as
before, and `TestHandleTaskHistory_Renders` covers the payload.

### The task-type dropdown is cached per tenant (2026-11-23)

`handleTaskHistory` ran `SELECT DISTINCT type FROM tasks WHERE tenant_id = ?`
on every page load. Even with a covering `(tenant_id, type)` index the scan
measured **~410 µs** over 4 000 tasks under the pure-Go SQLite driver (the plan
is `SEARCH tasks USING COVERING INDEX idx_tasks_tenant_type`). The list is now
cached per tenant for **60 s** (copy-on-read, bounded to 512 tenants); a newly
queued type appears within that window, which is fine for a filter dropdown.

`BenchmarkTaskList` (tenant-scoped / unscoped): **2.11 / 2.10 ms → 1.33 / 1.35 ms**
with the same response bytes; allocs 6 982 → 6 920.
`TestCachedTaskTypesPerTenant` pins tenant isolation and the TTL refresh,
`TestCachedTaskTypesReturnsCopy` pins the copy-on-read.

### The "retry all" flag is an index probe (2026-11-23)

The same page ran `SELECT COUNT(*) FROM tasks WHERE status = 'failed'` to decide
whether to show its "retry all" button, but only a boolean is consumed. A
`LIMIT 1` probe on the new `(tenant_id, status)` index measured **10.4 µs** vs
**231 µs** for the count over 4 000 tasks. No page-level number is claimed —
the seeded benchmark hits a failed row early, so the page delta sits inside the
run-to-run noise; the isolated query is the deterministic result.
`TestTaskHistoryHasFailedTasksFlag` pins both directions.

## 3. Deriving fleet capacity

A fleet of *N* implants on a *T*-second base interval produces `N/T` check-ins
per second. On the reference machine that costs, per core:

- idle check-ins: `N/T ÷ 1840` cores
- results on every check-in: `N/T ÷ 1010` cores

Worked examples on one core at a 30 s interval:

| Fleet | Idle | One result per check-in |
|---|---|---|
| 1 000 implants | 1.8% | 3.3% |
| 10 000 implants | 18% | 33% |
| 55 000 implants | ~100% | — |
| 30 000 implants | — | ~100% |

So a single modern core saturates somewhere around 55 000 idle implants on a
30 s interval, or 30 000 if every check-in carries a shell result — before
counting the database's own fsync cost, operator/UI polling, task fan-out and
plugin hooks, all of which share that core. Budget for headroom: the disk is
the usual bottleneck long before the CPU is.

Scaling out is by sharding fleets across teamservers (listeners are already
per-listener addressable), not by adding cores to one process: the SQLite
write path is deliberately single-writer, and `database.pool` stays 1 for it.

## 4. Profiling a running server (opt-in, loopback only)

Set a loopback address and restart:

```yaml
server:
  pprof_addr: "127.0.0.1:6060"   # empty (default) keeps profiling off
```

Then, from that host:

```bash
curl -s -o cpu.out  'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
curl -s -o heap.out 'http://127.0.0.1:6060/debug/pprof/heap'
go tool pprof -top -cum cpu.out
go tool pprof -top -cum heap.out
```

Properties, all enforced by tests in `internal/server/server_pprof_test.go`:

- **Off by default; loopback only.** A non-loopback `pprof_addr` is refused at
  config load (`config.Validate`) and again when the listener is bound, and the
  server refuses to start rather than serving profiles on a routable address.
  The endpoints have no authentication, so this is a fail-closed rule, not a
  warning.
- **Only the profiler is reachable.** The listener serves a dedicated mux with
  `/debug/pprof/*` and nothing else: the operator API, the SPA and the beacon
  routes are not exposed on that socket (`/api/v1/...`, `/health`, `/` answer 404).
- **Static setting.** `server.pprof_addr` is classified as a static reload
  token in the config status matrix, so a live edit reports "restart required"
  instead of silently doing nothing.
- **Bound at startup, closed at shutdown.** A port conflict fails the boot
  synchronously, and `Shutdown` closes the listener.

The `-cpuprofile` flag of the benchmark above is the other half of the loop:
profile the isolated path, then confirm on the running server.

## 5. SQL plan regressions

`internal/db/hot_path_index_test.go` measures `EXPLAIN QUERY PLAN` for every
query shape an index migration claims to fix: the pre-index plan must contain a
known-bad fragment (a full scan or a temp b-tree sort), and after the migration
the plan must use the index and stop sorting. An index nobody's query uses
fails the build.

The harness now supports one migration per probe (`hotPathProbe.migration`):
adding an index to an already-applied migration would leave every existing
database without it, because the runner records migrations as applied.

Current probes and where the queries come from:

- `idx_tasks_status_updated`, `idx_tasks_created_by_created`, `idx_alerts_tenant_created`,
  `idx_notifications_severity_created`, `idx_token_entries_agent_created`,
  `idx_build_logs_platform_created`, `idx_build_logs_status_created`,
  `idx_chat_messages_channel_created`, `idx_user_sessions_user_created`,
  `idx_api_keys_user_created` (batch of 2026-10-15)
- `idx_implants_listener_seen` (2026-10-16): the listener detail page lists an
  agent page as `WHERE listener_id = ? ORDER BY last_seen DESC LIMIT 5000`, and
  the active-agent counters reuse the predicate with a last_seen window. The
  single-column `idx_implants_listener_id` found the rows but the sort went to
  a temp b-tree (the LIMIT could not stop early), and the counters read
  `last_seen` off every row. The composite makes the page an ordered index walk
  and the counter an index-only range.

Tables are declared per probe (`creates`) with a baseline of the indexes a
production database already has (`pre`), because `AutoMigrate` alone only
creates the `gorm:"index"` tag ones. A probe whose "before" fragment no longer
appears fails the test: the query shape changed, and the index must be
re-examined rather than kept on faith.

## 6. Measured and rejected

Recorded so a future batch does not re-try it from the profile alone.

GORM's `PrepareStmt` (one prepared statement per distinct SQL text) looked like
the obvious answer to the CPU profile, which puts SQLite's parser
(`_yy_reduce`) at roughly a quarter of the check-in path. Measured A/B on the
three benchmarks above (2 runs each, same machine):

| Benchmark | without cache | with `PrepareStmt` |
|---|---|---|
| `BenchmarkBeaconCheckin` | 543k ns/op | 594k ns/op |
| `BenchmarkBeaconTaskDelivery` | 725k ns/op | 790k ns/op |
| `BenchmarkBeaconCheckinWithResult` | 993k ns/op | 1 005k ns/op |

It is 1–9% *slower* and allocates more (GORM's cache bookkeeping costs more
than parsing these short statements). Not shipped. The remaining parse cost is
structural — roughly half of `processBeacon` is `processAgentRegistration`,
which upserts the agent inside a transaction on every check-in — so any real
win there is a behaviour change to the registration path, not a config flag.

## 7. What these numbers do not include

- **Disk.** The benchmarks use in-memory SQLite; a file-backed database adds
  WAL fsync per write transaction. Expect materially lower throughput for
  result-heavy fleets on spinning or network storage.
- **Concurrency.** The benchmarks are single-goroutine: no lock contention, no
  GOMAXPROCS effects, no SQLite busy-retry pacing.
- **Network.** `httptest` records the response in memory; kernel time, TLS
  handshakes and per-connection overhead are outside the measurement.
- **The rest of the server.** Operator UI polling, plugin hooks, event fan-out,
  AI context builders and cleanup loops all share the same core.

## 8. The size of the binary

The published binary is **59.5 MiB** (linux/amd64, built with `-s -w`). The
unstripped build of the same code is 80.7 MiB; the difference is the symbol
table and the DWARF data, nothing else. The release pipeline strips, so a
download is 59.5 MiB and the one-click archives shrink by the same. (The 84 MiB
figure that circulates is the **unstripped Windows** build left in a working
directory, not the published artifact.)

Where the size actually is, measured with `node scripts/binary-size.mjs --build`
(which attributes symbol sizes to the modules that own them):

| Part | server (linux/amd64) | implant (linux/amd64) |
|---|---|---|
| Go runtime + standard library | 41.3 MB | 35.1 MB |
| This repository (code + the embedded console) | 4.8 MB | — |
| `github.com/ugorji/go` (gin's JSON codec) | 3.1 MB | — |
| `golang.org/x/text` | 1.6 MB | — |
| `github.com/dop251/goja` (JS scripting) | 1.4 MB | — |
| `github.com/jackc/pgx` (Postgres driver) | 1.3 MB | — |
| `github.com/tetratelabs/wazero` (WASM plugins) | 1.1 MB | — |
| **Pion (WebRTC / TURN / DTLS / STUN)** | **0.1 MB** | **~1.1 MB** |

The Pion row is the one worth writing down, because "the WebRTC dependency chain
is what makes the binary huge" costs an afternoon to disprove. On the server the
whole Pion tree is **0.1 MB of 59.5 MB** — the server links the TURN/DTLS/STUN
half (it is the TURN server); the ICE/RTP half belongs to the implant, where it
is ~1.1 MB of a 26.3 MB stripped binary. Isolating Pion behind a build tag would
save ~0.2% of the server binary, break remote desktop for everyone who builds
from source, and split the release matrix. It is not worth doing.

Reproduce the numbers:

```bash
node scripts/binary-size.mjs --build          # build + profile (linux/amd64)
node scripts/binary-size.mjs path/to/binary    # profile a binary you already have
```

The release job also carries a **70 MiB budget** for the published binary: a
heavy dependency arriving unnoticed fails the release instead of silently
shipping 20 MB more to every download. The frontend has the same kind of
budget (`check:bundle`).
