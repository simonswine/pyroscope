# Query Fairness for the V2 Read Path — Design Document

**Status**: Draft
**Date**: 2026-04-01

---

## Problem Statement

The V2 read path (`Frontend → Query Backend`) has no per-tenant fairness:

1. **No per-tenant scheduling**: The only protection is a global adaptive concurrency limiter (gradient2, 50–100 concurrent requests) on each query backend instance. There is no per-tenant awareness — a single tenant can consume all slots.

2. **No global outstanding limits**: There is no mechanism to cap how many concurrent queries a tenant can have in-flight across all frontends.

3. **No cross-frontend coordination**: Each frontend dispatches independently. If a tenant floods one frontend, there's no signal to other frontends about the overload.

## Scope

This design covers **only the V2 read path**. The V1 read path (Frontend → Scheduler → Querier) is unchanged — its existing in-process queue, round-robin scheduling, and shuffle sharding remain as-is.

---

## Goals

1. **Global per-tenant admission control**: Outstanding request limits enforced globally via Valkey.
2. **Fair scheduling across tenants**: Round-robin dispatch prevents one tenant from starving others.
3. **Fair scheduling within tenants**: Round-robin across users/actors prevents one user from starving others in the same org.
4. **Preserve direct dispatch**: No new scheduler hop — frontends continue to call `QueryBackend.Invoke()` directly.
5. **Incremental rollout**: Behind a feature flag, with graceful fallback when Valkey is unavailable.

## Non-Goals

- Changes to the V1 read path.
- Write-path fairness (already has per-tenant rate limiting).
- Query cost estimation / weighted fairness (future extension).

---

## Architecture Overview

A **FairDispatcher** is added to the query-frontend, sitting between the V2 read path router and the query backend client. It enforces per-tenant and per-actor admission control and fair scheduling, using Valkey for cross-frontend coordination.

```
                              ┌──────────────┐
                              │    Valkey     │
                              │              │
                              │ • outstanding │
                              │   counters   │
                              │ • tenant     │
                              │   priority   │
                              └──────┬───────┘
                                     │
              ┌──────────────────────┼──────────────────────┐
              │                      │                       │
        ┌─────┴──────┐        ┌─────┴──────┐         ┌─────┴──────┐
        │ Frontend-1  │        │ Frontend-2  │         │ Frontend-3  │
        │             │        │             │         │             │
        │ ┌─────────┐ │        │ ┌─────────┐ │         │ ┌─────────┐ │
        │ │  Fair    │ │        │ │  Fair    │ │         │ │  Fair    │ │
        │ │Dispatcher│ │        │ │Dispatcher│ │         │ │Dispatcher│ │
        │ └────┬─────┘ │        │ └────┬─────┘ │         │ └────┬─────┘ │
        └──────┼───────┘        └──────┼───────┘         └──────┼───────┘
               │                       │                        │
               ▼                       ▼                        ▼
          Query Backends          Query Backends           Query Backends
          (direct gRPC)           (direct gRPC)            (direct gRPC)
```

**Integration point**: The FairDispatcher implements the `QueryBackend` interface. `QueryFrontend.doQuery()` calls `dispatcher.Invoke()` instead of `backend.Invoke()` directly. From the caller's perspective, the dispatcher is a drop-in replacement for the query backend client.

---

## Detailed Design

### 1. Request Admission — Global Per-Tenant Outstanding Limit

Every V2 query must pass through Valkey-backed admission before dispatch. The frontend atomically increments a global counter; if it exceeds the tenant's limit, the request is rejected with HTTP 429.

**Valkey key**: `pyroscope:outstanding:{tenant_id}` (integer, TTL = query timeout × 2)

```lua
-- ADMIT.lua
-- NOTE: Lua scripts execute atomically in Valkey — no race between GET and INCR.
local key = KEYS[1]                    -- pyroscope:outstanding:{tenant_id}
local max = tonumber(ARGV[1])          -- max_outstanding_per_tenant
local ttl = tonumber(ARGV[2])          -- ttl seconds

local current = tonumber(redis.call("GET", key) or "0")
if current >= max then
    return 0  -- REJECTED
end
redis.call("INCR", key)
redis.call("EXPIRE", key, ttl)
return 1  -- ADMITTED
```

On request completion (success, error, or cancellation):

```lua
-- RELEASE.lua
local key = KEYS[1]
local val = redis.call("DECR", key)
if val <= 0 then
    redis.call("DEL", key)
end
```

**Config**: `max_outstanding_requests` per-tenant override (default: 100).

---

### 2. Frontend Fair Dispatcher

Each frontend runs a **FairDispatcher** that mediates V2 query dispatches. It enforces per-tenant concurrency limits and two-level round-robin ordering (tenant → actor) locally, with Valkey providing global coordination signals.

#### Data Structures

The dispatcher maintains a two-level queue: tenants contain per-actor sub-queues. When no `X-Actor-Path` header is present, requests go into a default `actor=0` sub-queue (see section 4 for details on actor identity).

```go
type FairDispatcher struct {
    valkey     ValkeyClient
    health     *ValkeyHealth
    limits     Limits
    backend    querybackend.QueryBackend  // underlying backend client

    mu             sync.Mutex
    tenants        map[string]*tenantQueue
    tenantOrder    []string  // tenant IDs for round-robin iteration
    nextTenantIdx  int
    wakeup         chan struct{}

    // Total concurrent dispatches across all tenants from this frontend
    semaphore  chan struct{}  // buffered, cap = max-concurrent-dispatches
}

type tenantQueue struct {
    actors   map[uint32]*actorQueue  // keyed by actor hash (0 = unknown/no header)
    order    []uint32                // actor hashes for round-robin within tenant
    nextIdx  int
    inflight atomic.Int32
}

type actorQueue struct {
    pending  list.List  // FIFO of *pendingRequest
    inflight atomic.Int32
}
```

#### Local Concurrency Limiting

The dispatcher limits how many concurrent `backend.Invoke()` calls each tenant can have in-flight from this frontend:

```
local_budget = max_outstanding_requests / num_frontends
```

Where `num_frontends` is discovered via DNS or ring. This is approximate — during scale-up (e.g., 5→10 frontends), existing frontends don't immediately halve their budget, so the effective global limit may temporarily reach ~1.5× the configured value. This is acceptable because the hard limit is enforced globally via Valkey; the local budget is a soft optimization to spread load.

#### Two-Level Round-Robin Dispatch

When dispatch capacity becomes available, the dispatcher picks the next request using round-robin across tenants (level 1), then round-robin across actors within that tenant (level 2):

```go
func (d *FairDispatcher) dispatchLoop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case d.semaphore <- struct{}{}: // acquire global slot
        }

        req := d.pickNextRequest()
        if req == nil {
            <-d.semaphore // release, nothing to dispatch
            d.waitForWork(ctx)
            continue
        }

        go d.dispatch(ctx, req)
    }
}

func (d *FairDispatcher) pickNextRequest() *pendingRequest {
    d.mu.Lock()
    defer d.mu.Unlock()

    // Level 1: round-robin across tenants
    for i := 0; i < len(d.tenantOrder); i++ {
        idx := (d.nextTenantIdx + i) % len(d.tenantOrder)
        tenantID := d.tenantOrder[idx]
        tq := d.tenants[tenantID]

        if tq.totalPending() == 0 {
            continue
        }
        if tq.inflight.Load() >= d.localBudget(tenantID) {
            continue
        }

        // Level 2: round-robin across actors within this tenant
        req := tq.pickNextActor()
        if req != nil {
            d.nextTenantIdx = idx + 1
            tq.inflight.Add(1)
            return req
        }
    }
    return nil
}
```

#### Context Cancellation

Queued requests are associated with their request context. A background goroutine periodically scans pending queues and removes requests whose context has been cancelled (client disconnected). This prevents stale requests from accumulating when users cancel and re-issue queries:

```go
func (d *FairDispatcher) reapCancelled() {
    d.mu.Lock()
    defer d.mu.Unlock()

    for _, tq := range d.tenants {
        for _, aq := range tq.actors {
            for e := aq.pending.Front(); e != nil; {
                next := e.Next()
                req := e.Value.(*pendingRequest)
                if req.ctx.Err() != nil {
                    aq.pending.Remove(e)
                    d.releaseOutstanding(req.tenantID)
                }
                e = next
            }
        }
    }
}
```

This runs every 250ms. Additionally, when the dispatch loop picks a request, it checks `req.ctx.Err()` before dispatching — if cancelled, it skips and picks the next.

#### Dispatch to Query Backend

Once picked, the request goes directly to the query backend — same as today, just mediated by the dispatcher:

```go
func (d *FairDispatcher) dispatch(ctx context.Context, req *pendingRequest) {
    defer func() {
        <-d.semaphore
        d.tenants[req.tenantID].inflight.Add(-1)
        req.actorQueue.inflight.Add(-1)
        d.releaseOutstanding(req.tenantID) // Valkey RELEASE.lua
        d.wakeDispatchLoop()
    }()

    resp, err := d.backend.Invoke(ctx, req.invokeRequest)
    req.resultCh <- dispatchResult{resp: resp, err: err}
}
```

---

### 3. Tenant Priority Tracking (Valkey)

To achieve **cross-frontend** fairness (not just per-frontend round-robin), the dispatcher reads a global priority signal from Valkey. This tracks how much service each tenant has received across all frontends.

**Valkey key**: `pyroscope:served` → Sorted Set `{tenant_id: total_served_count}`

On each dispatch, the frontend increments the tenant's served count:

```lua
-- SERVED.lua (fire-and-forget, non-blocking)
local key = KEYS[1]                    -- pyroscope:served
local tenant_id = ARGV[1]
redis.call("ZINCRBY", key, 1, tenant_id)
```

Periodically (every 1s), each frontend reads the served counts and adjusts its local round-robin to **prioritize under-served tenants**:

```go
func (d *FairDispatcher) refreshPriority(ctx context.Context) {
    // Get tenants sorted by served count (ascending = least served first)
    served, _ := d.valkey.ZRangeWithScores(ctx, "pyroscope:served", 0, -1)

    d.mu.Lock()
    d.reorderTenants(served) // least-served tenants first in round-robin
    d.mu.Unlock()
}
```

The served counts are reset periodically (every 60s via TTL) to prevent stale tenants from permanently affecting priority.

This is a **soft coordination signal** — each frontend independently decides dispatch order, but they converge on globally fair behavior because they all observe the same Valkey state.

---

### 4. Sub-Tenant User Fairness

Cross-tenant fairness (sections 1–3) prevents one tenant from starving another. But within a single large tenant, one user (e.g., a Grafana dashboard with auto-refresh, or an automation script) can monopolize all of that tenant's query budget, starving other users in the same org. Sub-tenant fairness addresses this.

#### 4.1 The Actor Identity Problem

Today, Pyroscope has **no sub-tenant identity on the read path**. The only identity is the tenant ID from `X-Scope-OrgID`. There is no header or context that identifies which Grafana user or API caller initiated a query.

**Existing precedent in the gateway**: The GE gateway (`backend-enterprise/pkg/gateway/middleware`) already implements this pattern for Loki:

1. Grafana sends `X-Grafana-User` header on datasource proxy requests (when `send_user_header = true` in Grafana config).
2. The gateway's `UserHashingMiddleware` converts `X-Grafana-User` into an opaque `X-Loki-Actor-Path` header (FNV32 hash of the username).
3. Loki's scheduler uses this actor path for hierarchical queue placement.

**Current gap for Pyroscope**: The gateway's `PyroscopeQueryRoutes` do **not** include the `LokiUserPass()` route option. The `X-Grafana-User` header is present in the request (sent by Grafana) but is neither translated nor forwarded to Pyroscope. The header is silently dropped.

#### 4.2 Actor Path Header

We reuse the existing Loki header for consistency:

```
X-Loki-Actor-Path: <hashed-user-id>
```

Despite the Loki-specific name, this header is already produced by the gateway's `UserHashingMiddleware` and uses a generic FNV32 hash. Renaming it to a backend-agnostic name (e.g., `X-Actor-Path`) is a desirable follow-up in the gateway, but for the initial implementation we accept the existing header to avoid coordinating a rename across Loki, the gateway, and Pyroscope simultaneously.

Using a hash rather than the raw username:
- Avoids leaking PII into logs, metrics, and traces.
- Is consistent with Loki's existing approach (same hash function).
- Allows the fair dispatcher to treat it as an opaque bucket key.

#### 4.3 Propagation Chain

```
Grafana (send_user_header=true)
  |
  |  X-Grafana-User: alice@grafana.com
  v
GE Gateway
  |
  |  UserHashingMiddleware (needs to be enabled for Pyroscope routes)
  |  X-Grafana-User -> X-Loki-Actor-Path: 2837491 (FNV32 hash)
  v
Pyroscope Query Frontend
  |
  |  Extract X-Loki-Actor-Path from request headers
  |  Use as sub-queue key within tenant's queue
  v
FairDispatcher
  |
  |  Two-level round-robin:
  |    Level 1: across tenants
  |    Level 2: across actors within each tenant
  v
Query Backend
```

**Required changes outside Pyroscope**:

1. **GE Gateway**: Add `LokiUserPass()` to `PyroscopeQueryRoutes`. This is a one-line change per route — the same option Loki routes already use.

2. **Grafana**: Ensure `send_user_header = true` is enabled (it is by default in recent Grafana versions).

#### 4.4 Two-Level Queue Structure

The FairDispatcher's per-tenant queue is a two-level structure (already shown in section 2's data structures):

```
FairDispatcher
+-- Tenant-A
|   +-- Actor 2837491 (alice)   [req1, req5]
|   +-- Actor 9182736 (bob)     [req2]
|   +-- Actor 0 (unknown)       [req7]      <- no actor header
+-- Tenant-B
|   +-- Actor 5551234 (carol)   [req3, req4]
|   +-- Actor 0 (unknown)       [req6]
```

The dispatch algorithm (section 2) already handles this: level 1 round-robins across tenants, level 2 round-robins across actors within the selected tenant.

#### 4.5 Actor-Level Admission (Optional Enhancement)

Beyond fair ordering, we can enforce per-actor outstanding limits via Valkey:

**Valkey key**: `pyroscope:outstanding:{tenant_id}:{actor_hash}` (integer, TTL = query timeout × 2)

This prevents a single user from consuming the tenant's entire outstanding budget. The limit is a direct per-tenant override — not derived from another value:

**Config**: `max_outstanding_requests_per_actor` per-tenant override (default: 0 = no per-actor limit, only tenant-level).

For example, if a tenant's total budget is 100 and `max_outstanding_requests_per_actor` is 30, any single user can have at most 30 concurrent queries, ensuring at least 3 users can run concurrently at full capacity.

#### 4.6 Behavior Without Actor Header

When `X-Loki-Actor-Path` is absent (direct API calls, older Grafana versions, `send_user_header=false`, gateway without `LokiUserPass`), the request is placed in a default `actor=0` (unknown) queue within the tenant. This queue participates in the same round-robin as named actors.

This means: if one user has the header and another doesn't, they get fair scheduling between them. If nobody has the header, all requests land in the same `actor=0` queue and get FIFO ordering — same as today.

**No degradation**: The system works correctly with zero, partial, or full actor header coverage.

---

### 5. Behavior When Valkey Is Unavailable

Valkey is in the admission control hot path. The system must handle Valkey failures without blocking queries.

#### 5.1 Health Detection

Each frontend tracks Valkey health based on operation outcomes:

```go
type ValkeyHealth struct {
    state              atomic.Int32  // 0=healthy, 1=degraded, 2=unavailable
    consecutiveFails   atomic.Int32
    lastSuccess        atomic.Int64  // unix nanos
}
```

State transitions:

```
healthy --(3 consecutive failures)--> degraded --(10 consecutive failures)--> unavailable
   ^                                      |                                       |
   +----------(1 success)----------------+                                       |
   ^                                                                              |
   +----------(1 success on probe)-----------------------------------------------+
```

- **Healthy**: All Valkey operations attempted normally (200ms timeout).
- **Degraded**: Valkey operations attempted with short timeout (50ms). If timed out, that single request uses local fallback — no state change.
- **Unavailable**: Valkey operations skipped entirely on the hot path. Background probe every 1s. On first successful probe -> healthy.

#### 5.2 Per-Feature Fallback

Each Valkey-backed feature degrades independently:

| Feature | Normal (Valkey healthy) | Fallback (Valkey unavailable) |
|---|---|---|
| **Admission control** | Global counter: `max / 1` | Local counter: `max / num_frontends` |
| **Fair dispatch ordering** | Priority-weighted round-robin (Valkey served counts) | Pure local round-robin (no cross-frontend coordination) |
| **Sub-tenant fairness** | Two-level round-robin (local, no Valkey needed) | No change (actor queues are entirely local) |
| **Per-actor admission** | Global per-actor counter | Local per-actor counter (same division as tenant) |

**Key point**: The local fair dispatcher (two-level round-robin, concurrency limits) works entirely without Valkey. Only the *cross-frontend coordination* degrades. Per-frontend fairness — including sub-tenant actor fairness — is always active.

#### 5.3 Admission Control Fallback

The design **fails open with tightened local limits** — queries are not rejected due to Valkey being down, but per-frontend limits compensate for the loss of global coordination.

```go
func (a *AdmissionController) Admit(ctx context.Context, tenantID string) (release func(), err error) {
    globalMax := a.limits.MaxOutstandingRequests(tenantID)

    switch a.health.State() {
    case Healthy, Degraded:
        return a.admitViaValkey(ctx, tenantID, globalMax)
    case Unavailable:
        return a.admitLocal(tenantID, globalMax)
    }
}

func (a *AdmissionController) admitLocal(tenantID string, globalMax int) (func(), error) {
    localMax := max(1, globalMax/a.numFrontends())

    counter := a.getOrCreateCounter(tenantID)
    if counter.Add(1) > int32(localMax) {
        counter.Add(-1)
        return nil, ErrTooManyRequests
    }

    return func() { counter.Add(-1) }, nil
}
```

**Why `globalMax / numFrontends`?** If the global limit is 100 and there are 5 frontends, each allows 20 locally. If load is balanced, the global total is 100. If load is skewed, the worst case is one frontend allows 20 while the global effective limit is lower. This deliberately prefers availability over precision — queries keep flowing during Valkey outages.

**Configurable policy**:

```
-query-frontend.fairness.unavailable-policy=fail-open      # default: local limits
-query-frontend.fairness.unavailable-policy=fail-closed     # reject all new queries
-query-frontend.fairness.unavailable-policy=pass-through    # no limits, rely on backend gradient2
```

#### 5.4 Outstanding Counter Drift & Self-Healing

When Valkey goes down mid-flight, counters can leak:

```
Frontend increments counter -> Valkey goes down -> query completes ->
frontend can't decrement -> Valkey comes back -> counter too high -> false rejections
```

**TTL-based self-healing**: Outstanding counters have TTL = `query_timeout x 2`. Leaked counters expire naturally. The TTL is refreshed on every successful increment, so active counters survive.

**Reconciliation on recovery**: When transitioning from `unavailable` -> `healthy`, the frontend does not force-correct Valkey counters. Instead it relies on TTL expiry (typically ~60s). This avoids race conditions where multiple frontends try to reconcile simultaneously.

#### 5.5 Split-Brain: Some Frontends Can Reach Valkey, Others Cannot

- Frontends that can reach Valkey -> global limits (Valkey-backed).
- Frontends that cannot -> local limits (`globalMax / numFrontends`).
- Combined outstanding could temporarily exceed the intended global limit.

**Worst-case over-admission** (K healthy, N-K partitioned frontends):

```
max_outstanding <= globalMax + (N - K) x (globalMax / N)
               <= 2 x globalMax
```

The global limit is at most doubled during a partial partition. This is acceptable because:
1. Partitions are transient.
2. Backend gradient2 limiters provide a hard floor.
3. The alternative (rejecting all queries on partitioned frontends) causes unnecessary failures.

#### 5.6 Valkey Restart / Data Loss

| Data | Effect of loss | Recovery |
|---|---|---|
| Outstanding counters | Momentary burst of admissions until counters rebuild from traffic | Self-corrects in seconds |
| Served counts (priority) | Priority resets to uniform across tenants | Self-corrects within TTL (60s) |
| Per-actor counters | Per-actor limits temporarily unenforced | Self-corrects in seconds |

No queries are lost or corrupted. The system is **eventually consistent** with respect to fairness — it converges to correct behavior within one TTL window.

#### 5.7 Frontend Startup Without Valkey

The frontend starts successfully in local-only mode. Background probe runs. When Valkey becomes available, transitions to global mode. No manual intervention. **Valkey is not a startup dependency.**

#### 5.8 Latency Impact

Valkey operations are in the hot path:

| Operation | When | Expected latency | Timeout |
|---|---|---|---|
| `ADMIT.lua` | Every V2 request | < 1ms (local DC) | 200ms (healthy), 50ms (degraded) |
| `RELEASE.lua` | Every V2 completion | < 1ms | Fire-and-forget with async retry |
| `SERVED.lua` | Every V2 dispatch | < 1ms | Fire-and-forget |
| `ZRANGE` priority read | Every 1s per frontend | < 2ms | 500ms |

If Valkey is slow but not down: the degraded state (50ms timeout) prevents Valkey latency from adding to query latency. Worst case is ~50ms on a single request, then that request uses local fallback.

#### 5.9 Failure Mode Matrix

| Failure | Detection | Behavior | Recovery | Impact Duration |
|---|---|---|---|---|
| **Valkey briefly slow** (< 3 ops) | Per-op timeout | Individual ops fall back to local | Automatic (next success) | ~50ms per affected request |
| **Valkey degraded** (3+ timeouts) | Consecutive failures | Short timeouts, per-op fallback | 1 success -> healthy | Seconds |
| **Valkey down** (10+ failures) | Consecutive failures | Full local-only mode | Probe success -> healthy | Until Valkey recovers |
| **Valkey data loss** | Transparent | Counters rebuild from traffic | Self-healing via TTL | ~60s reduced fairness |
| **Network partition** | Per-frontend | Mixed global/local mode | Per-frontend recovery | Until partition heals |
| **Valkey unavailable at startup** | Initial connect | Start in local-only mode | Background probe | Until Valkey available |

#### 5.10 Observability

| Metric | Type | Description |
|---|---|---|
| `pyroscope_query_frontend_valkey_state` | Gauge | 0=healthy, 1=degraded, 2=unavailable |
| `pyroscope_query_frontend_valkey_fallback_total` | Counter `{feature}` | Times each feature fell back to local |
| `pyroscope_query_frontend_valkey_duration_seconds` | Histogram `{op}` | Valkey call latency |
| `pyroscope_query_frontend_valkey_errors_total` | Counter `{op}` | Failed Valkey operations |
| `pyroscope_query_frontend_admission_mode` | Gauge | 0=global, 1=local |

**Alerts**:
- `PyroscopeValkeyUnavailable`: `valkey_state == 2` for > 1m. Fairness fully degraded to per-frontend.
- `PyroscopeValkeyDegraded`: `valkey_state == 1` for > 5m. Valkey slow, investigate.
- `PyroscopeAdmissionFallbackActive`: `admission_mode == 1` on any frontend. Global limits not enforced.

---

## Request Flow Summary

```
1. V2 query arrives at Frontend (via readpath Router)
2. Extract tenant ID and actor hash (from X-Loki-Actor-Path header, default 0)
3. ADMISSION: Run ADMIT.lua on Valkey (or local fallback)
   +-- If rejected -> HTTP 429
4. ENQUEUE: Place request in FairDispatcher's per-tenant, per-actor queue
5. DISPATCH: FairDispatcher picks next request via two-level round-robin:
   +-- Level 1: round-robin across tenants (priority-weighted)
   +-- Level 2: round-robin across actors within selected tenant
   +-- Call QueryBackend.Invoke() directly
6. COMPLETE: Run RELEASE.lua on Valkey (fire-and-forget)
7. Return response to caller
```

---

## Valkey Data Model

| Key Pattern | Type | Description | TTL |
|---|---|---|---|
| `pyroscope:outstanding:{tenant}` | String (int) | Global outstanding request count per tenant | query_timeout x 2 |
| `pyroscope:outstanding:{tenant}:{actor}` | String (int) | Outstanding count per actor within tenant (optional, requires `max_outstanding_requests_per_actor > 0`) | query_timeout x 2 |
| `pyroscope:served` | Sorted Set | Served count per tenant (cross-frontend priority signal) | 60s |

**Estimated Valkey memory**: ~100 bytes per active tenant + ~50 bytes per active actor (when per-actor admission is enabled). At 10K tenants with 10 actors each: ~6 MB. Request payloads never enter Valkey.

---

## Configuration

### New Flags

| Flag | Default | Description |
|---|---|---|
| `-query-frontend.fairness.enabled` | `false` | Enable Valkey-backed fair dispatching for V2 |
| `-query-frontend.fairness.valkey-address` | — | Valkey address (e.g., `valkey:6379`) |
| `-query-frontend.fairness.valkey-cluster` | `false` | Valkey Cluster mode |
| `-query-frontend.fairness.valkey-tls` | `false` | TLS for Valkey |
| `-query-frontend.fairness.valkey-password` | — | Auth password |
| `-query-frontend.fairness.valkey-pool-size` | `10` | Connection pool size |
| `-query-frontend.fairness.valkey-key-prefix` | `pyroscope` | Key prefix for multi-cluster isolation |
| `-query-frontend.fairness.max-concurrent-dispatches` | `100` | Total concurrent V2 dispatches per frontend |
| `-query-frontend.fairness.priority-refresh-interval` | `1s` | How often to read global priority from Valkey |
| `-query-frontend.fairness.served-counter-ttl` | `60s` | TTL for priority counter reset |
| `-query-frontend.fairness.unavailable-policy` | `fail-open` | Behavior when Valkey is down: `fail-open`, `fail-closed`, `pass-through` |
| `-query-frontend.fairness.actor-header` | `X-Loki-Actor-Path` | Header name for sub-tenant actor identity |

### New Per-Tenant Overrides

| Override | Default | Description |
|---|---|---|
| `max_outstanding_requests` | `100` | Global outstanding limit for V2 queries |
| `max_outstanding_requests_per_actor` | `0` | Per-actor outstanding limit (0 = no per-actor limit) |

---

## Observability

### Metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `pyroscope_query_frontend_outstanding_requests` | Gauge | `tenant` | Global outstanding per tenant |
| `pyroscope_query_frontend_queue_length` | Gauge | `tenant` | Requests waiting in local fair queue |
| `pyroscope_query_frontend_queue_duration_seconds` | Histogram | — | Time spent waiting in fair queue |
| `pyroscope_query_frontend_dispatch_total` | Counter | `tenant` | Total V2 dispatches |
| `pyroscope_query_frontend_rejected_total` | Counter | `tenant`, `reason` | Rejected requests (429) |
| `pyroscope_query_frontend_tenant_priority` | Gauge | `tenant` | Tenant position in priority order |
| `pyroscope_query_frontend_actor_inflight` | Gauge | `tenant`, `actor` | Inflight requests per actor (high-cardinality, sampled) |

**Note on actor metrics**: The `actor` label is a numeric hash, not a username — no PII risk. However, cardinality can be high with many active actors. This metric should be opt-in or sampled (e.g., top-N actors per tenant only).

---

## Fairness Layers Summary

| Layer | Mechanism | Scope | Key Config |
|---|---|---|---|
| **Admission** | Global per-tenant counter -> HTTP 429 | Global (Valkey) | `max_outstanding_requests` |
| **Cross-tenant ordering** | Round-robin across tenants, priority-weighted | Per-frontend (Valkey-coordinated) | — |
| **Sub-tenant ordering** | Round-robin across actors within tenant | Per-frontend (local) | `X-Loki-Actor-Path` header |
| **Per-actor admission** | Global per-actor counter -> HTTP 429 (optional) | Global (Valkey) | `max_outstanding_requests_per_actor` |
| **Backend concurrency** | Adaptive gradient2 limiter | Per-backend instance | (existing) |

---

## Migration Path

### Phase 1: Global Admission Control

Add Valkey-backed outstanding counters in the V2 frontend path. No changes to dispatch ordering — just admission check before calling `backend.Invoke()`.

**Impact**: Prevents tenant monopolization. Minimal code change.
**Risk**: Low — additive, behind feature flag, fallback to local counters.

### Phase 2: Fair Dispatcher

Add the FairDispatcher with local round-robin. V2 queries go through the fair queue before dispatch. Initially single-level (tenant only, no actor sub-queues).

**Impact**: Per-tenant fairness on V2 for the first time.
**Risk**: Low — behind flag, local-only (no Valkey dependency for this phase).

### Phase 3: Sub-Tenant User Fairness

Add two-level round-robin (tenant -> actor). Requires `X-Loki-Actor-Path` header propagation from GE gateway.

**Impact**: Prevents one user from starving others within the same tenant.
**Risk**: Low — graceful degradation when header is absent (all requests land in `actor=0` queue).

**Dependency**: GE gateway change to add `LokiUserPass()` to `PyroscopeQueryRoutes` (one-line per route).

### Phase 4: Cross-Frontend Priority Coordination

Add Valkey-backed served counter and priority-weighted round-robin.

**Impact**: Cross-frontend fairness convergence.
**Risk**: Low — soft signal, degrades gracefully.

---

## Comparison with Mimir

| Aspect | Mimir | Pyroscope V2 (Proposed) |
|---|---|---|
| **Queue location** | In-process (per scheduler) | Frontend (local) + Valkey (coordination) |
| **Fairness scope** | Per-scheduler instance | Cross-frontend (Valkey-coordinated) |
| **Dispatch** | Scheduler -> Querier (gRPC stream) | Frontend -> Backend (direct gRPC) |
| **Request payloads** | In queue memory | Never in Valkey |
| **Sub-tenant fairness** | N/A | Two-level round-robin (tenant -> actor) |
| **Shuffle sharding** | In-process recomputation | Future work (backends fan out internally) |
| **Valkey dependency** | None | Soft (graceful fallback) |

**Note**: Loki (not Mimir) has a related actor-path queue mechanism using the same `X-Loki-Actor-Path` header proposed here. This design is inspired by Loki's approach.

---

## Alternatives Considered

### A. Full Valkey Queue (Scheduler for V2)

Route V2 queries through a Valkey-backed scheduler, analogous to V1.

- **Pro**: True global queue, exact fairness.
- **Con**: Adds network hop to V2 path. More complex. Payloads in Valkey.
- **Decision**: Rejected — V2 path should remain direct.

### B. Token Bucket Rate Limiting Only

Per-tenant requests/sec limit via Valkey, no fair queuing.

- **Pro**: Very simple.
- **Con**: Doesn't handle burst fairness or dispatch ordering.
- **Decision**: Possible Phase 0, but insufficient alone.

### C. No Valkey, Local-Only Fair Dispatcher

Fair dispatch per-frontend without any shared state.

- **Pro**: No new dependency.
- **Con**: No global limits. Per-frontend fairness only (no cross-frontend coordination).
- **Decision**: This is the fallback mode when Valkey is unavailable. Not sufficient as the primary design.

---

## Future Work

### Backend Shuffle Sharding

Shuffle-sharding tenants across a subset of query backend instances (like Mimir does for queriers) could reduce blast radius. However, query backends fan out to each other internally during query execution — a query hitting backend-A may cause backend-A to read from backend-B. This means sharding at the frontend level doesn't truly isolate load. Effective shuffle sharding would require changes to the query backend's internal fan-out, which is a larger effort.

### Query Cost Weighting

Pure round-robin treats all queries equally, but a query spanning 24h is far more expensive than one spanning 5m. Weighting fairness by estimated query cost (time range x label cardinality) would be more equitable. This requires a cost estimation model and changes to the served-count tracking.

### Dashboard / Panel Attribution

Beyond user-level fairness, attributing queries to specific dashboards and panels (via `X-Dashboard-UID` and `X-Panel-Id` headers from Grafana) would enable:
- Per-dashboard rate limiting (cap auto-refresh heavy dashboards)
- Observability into which dashboards generate the most query load
- Potential priority levels (interactive queries > background refresh)

### Generalized Actor Header

The `X-Loki-Actor-Path` header name is Loki-specific. A follow-up in the GE gateway should rename this to a backend-agnostic name (e.g., `X-Actor-Path`) and have all backends (Pyroscope, Mimir, Loki) accept the new header. The `actor-header` config flag in this design allows Pyroscope to accept either name without code changes.

---

## Open Questions

1. **Valkey HA**: Sentinel vs Cluster for production? Acceptable unavailability window?

2. **Multi-region**: Independent Valkey per region, or cross-region fairness?

3. **GE gateway generalization**: Should `UserHashingMiddleware` and `LokiUserPass` be generalized to a shared `ActorPass()` option usable by all backends, or should each backend have its own route option?

---

## Key Source Files

### Current

| Component | File |
|---|---|
| V2 query frontend | `pkg/frontend/readpath/queryfrontend/query_frontend.go` |
| V2 read path router | `pkg/frontend/readpath/router.go` |
| Query backend client | `pkg/querybackend/client/client.go` |
| Query backend concurrency | `pkg/querybackend/concurrency.go` |
| Per-tenant limits | `pkg/validation/limits.go` |

### Proposed

| Component | File |
|---|---|
| Fair dispatcher (two-level) | `pkg/frontend/fairness/dispatcher.go` |
| Admission controller | `pkg/frontend/fairness/admission.go` |
| Tenant priority tracker | `pkg/frontend/fairness/priority.go` |
| Actor extraction | `pkg/frontend/fairness/actor.go` |
| Valkey client & health | `pkg/frontend/fairness/valkey.go` |
| Lua scripts | `pkg/frontend/fairness/scripts/*.lua` |

### External Dependencies

| Component | Repo | Change Required |
|---|---|---|
| GE gateway Pyroscope routes | `backend-enterprise` | Add `LokiUserPass()` to `PyroscopeQueryRoutes` |
| Grafana user header | Grafana | Ensure `send_user_header = true` (default in recent versions) |

---

## References

- [Mimir Query Fairness](https://github.com/grafana/mimir) — Tree queue, shuffle sharding, component lanes
- [Loki Actor-Path Queues](https://github.com/grafana/loki) — `X-Loki-Actor-Path` header, hierarchical queue placement
- [GE Gateway UserHashingMiddleware](https://github.com/grafana/backend-enterprise/blob/main/pkg/gateway/middleware/user_hashing_middleware.go) — `X-Grafana-User` -> `X-Loki-Actor-Path` translation
- [Valkey Documentation](https://valkey.io/docs/) — Commands, Lua scripting, Pub/Sub
- [go-concurrency-limits](https://github.com/platinummonkey/go-concurrency-limits) — Gradient2 algorithm (used in query backend)
