# Persistent nodes and streaming SSH protocol

Status: proposed design. This document does not describe implemented behavior.

## Goals

- Use one persistent SSH connection per connected node for all managed TUI operations: preparation, uploads, worker control, events, and downloads.
- Communicate through a bidirectional, framed streaming stdio protocol, not repeated shell commands or SCP.
- Run multiple benchmarks sequentially on an existing EC2 node without reprovisioning it.
- Preserve remote work across controller disconnects and restarts.
- Preserve at-most-once ingestion per run, explicit cleanup, ownership checks, and atomic artifact publication.
- Keep the interactive shell (`h`) independent: it opens an additional SSH connection and does not interrupt the protocol connection.

## Non-goals for the first version

- Concurrent benchmark runs on one node. Benchmark CPU isolation requires exclusive use of the node.
- Automatically resuming a crashed benchmark midway through replay or measurement.
- Transparent reuse of ingested data between runs.
- A general-purpose remote shell or arbitrary command execution protocol.
- Multiple controllers mutating one node concurrently.
- Automatic EC2 termination when a benchmark finishes or the TUI exits.

## Current limitations

`remoteHost.ssh`, `output`, and `copy` create independent SSH/SCP processes. `followRemote` polls worker and systemd state every three seconds. A session combines AWS resources, preparation, execution, and results, so one instance effectively belongs to one run.

Remote paths such as `/tmp/benchmark`, `/tmp/results`, and `/var/lib/macro-benchmark/worker` are global. The global start marker intentionally prevents another ingestion, but also prevents intentional new runs on a retained node.

## Architecture

```text
Controller/TUI
  ├─ AWS API: discover, provision, resolve address, destroy
  ├─ node connection manager
  │    └─ one ssh -T process per connected node
  │          └─ privileged agent --stdio
  │                ├─ durable node/run metadata
  │                ├─ artifact store and event journals
  │                └─ systemd units: one worker per run
  │                       └─ worker subprocesses
  └─ h: separate interactive ssh process
```

The agent is a control process attached to SSH. The worker is a separate systemd-owned process. EOF or agent failure closes the control connection but does not kill the worker. Remote run truth lives on disk and in systemd, not in the agent's memory.

The controller has one connection manager per node, shared by upload, subscription, stop, and collection actions. Action cancellation must not cancel this shared connection. Disconnecting a node explicitly closes it; reconnecting creates a replacement, never another concurrent managed connection.

## Separate node and run models

### Node

A node owns:

- Stable node ID and local state format version.
- AWS region, instance ID, security group, imported key, ownership tags, and selected hardware configuration.
- SSH key reference, known-hosts file, and last resolved address.
- Agent protocol/build identity and last-observed capabilities.
- Optional absolute allocation deadline and retention policy.
- References to runs associated with it.

Provisioning state is separate from connection state. A disconnected node may still be running a benchmark. The UI must distinguish saved state from freshly observed remote state.

Example provisioning states: `draft`, `provisioning`, `ready`, `destroying`, `destroyed`, `error`.

Example connection states: `disconnected`, `connecting`, `bootstrapping`, `connected`, `incompatible`.

### Run

A run owns:

- Unique run ID and owning node ID once assigned.
- Immutable resolved commits, benchmark selection, dataset mapping, and measurement inputs.
- Bundle manifest/digest, controller provenance, and required agent capabilities.
- Absolute execution deadline, timestamps, phase/stage, and structured failure details.
- Event cursor, result manifest, and local collection state.

Run phases:

```text
draft → preparing → prepared → uploading → ready → starting → running
                                                        └─────┬─────┘
                                                              ↓
                             completed | failed | stopped | interrupted
```

Preparation/upload failures are retryable before start. Once a run has claimed its start marker, its identity is never reused for ingestion. “Rerun” clones inputs into a new run ID; it does not reset a terminal run.

Run phase and collection state are independent. A failed run can have a successfully collected diagnostic archive. Remote completion does not imply successful local download.

### Local layout

```text
$XDG_STATE_HOME/pyroscope-macro-benchmark/
  controller.lock
  controller.log
  nodes/<node-id>/state.json
  nodes/<node-id>/known_hosts
  runs/<run-id>/state.json
  bundles/<run-id>/...
  results/<run-id>/...
  ssh/...
```

Continue using private permissions, versioned snapshots, fsync, atomic rename, and the single local controller lock. Store references rather than duplicating mutable AWS ownership in every run.

## Remote layout and isolation

```text
/var/lib/macro-benchmark/
  node.json
  locks/control.lock
  locks/execution.lock
  agents/<digest>/macro-benchmark
  blobs/sha256/<digest>
  transfers/<transfer-id>/...
  runs/<run-id>/
    manifest.json
    bundle/...
    worker/started
    worker/status.json
    worker/events/...
    work/ingest/...
    work/minio/...
    private/...
    results/...
    results-manifest.json
    results.tar.gz
```

Replace global paths with a validated `RunPaths` object passed through worker execution, metrics collection, replay, and benchmark orchestration. Do not implement isolation by changing process-wide globals or the controller working directory.

Each run receives fresh MinIO storage, ingest storage, tenant mapping, credentials, endpoint manifests, and result directories. Credentials remain outside collected artifacts. Immutable binaries may be shared via the content-addressed blob store, but mutable data is never shared by default.

Use a validated systemd unit name such as `macro-benchmark-run-<run-id>.service`. The worker receives explicit run root and deadline arguments. Per-run units preserve process-tree ownership, graceful stop, and bounded forced termination.

Only one execution slot exists per node. Start and maintenance operations coordinate through an OS lock; the worker holds the execution lock for its full lifetime, including final artifact generation. A second start returns `node_busy` with the active run ID. Agent reconnection must not release this slot.

## Bootstrap using the same connection

A fresh node cannot execute an agent it has not received. Bootstrap must not rely on SCP or a second SSH session.

1. Resolve and verify the instance through AWS ownership checks.
2. Start one `ssh -T` process with existing host-key and keepalive policies.
3. Execute a small, fixed bootstrap helper using the supported Ubuntu image's Python 3 runtime. Verify that prerequisite explicitly; fail clearly if unavailable.
4. The helper emits a bounded bootstrap greeting on stdout. It reads a fixed-size bootstrap header containing the expected agent digest and byte length.
5. If the digest is already installed and verified, return `cached`. Otherwise return `send`, receive exactly the advertised bytes into a private temporary file, verify SHA256, fsync, and atomically install it.
6. The helper `exec`s the selected binary as `agent --stdio`. The SSH connection and stdin/stdout file descriptors are unchanged.
7. Perform the normal protocol handshake.

The bootstrap helper must use unbuffered exact reads so it cannot consume bytes belonging to the next protocol. Bootstrap lengths have hard limits. Never interpolate untrusted paths or run IDs into shell commands. Use `sudo -n`; prompting is an error. Bootstrap progress and diagnostics go to stderr, not stdout.

A failed connection may require a new attempt, but only one managed connection attempt is live per node. Do not retain the current SSH `true` polling loop after a successful connection.

Agent binaries are immutable and pinned per connection/run. Uploading a newer agent never overwrites a binary used by a running worker. Garbage collection preserves all referenced versions. Initially, unsupported protocol majors fail explicitly; there is no silent downgrade or automatic mid-run migration.

## Wire protocol v1

### Transport and framing

SSH provides authentication, encryption, and transport liveness. Allocate no PTY. Stdout is exclusively protocol frames; stderr is bounded diagnostic output.

Use a binary envelope:

```text
uint32 big-endian header length
uint32 big-endian payload length
UTF-8 JSON header
raw payload bytes
```

Initial limits: 64 KiB JSON header and 1 MiB payload per frame. Reject oversized frames before allocating. Read headers and payloads with exact-read semantics. Unknown major versions, invalid framing, and truncated frames terminate the connection. Method-level validation errors produce a structured response without corrupting the stream.

Header fields include:

- `version`: selected protocol major version.
- `kind`: `request`, `response`, `event`, `chunk`, `credit`, or `cancel`.
- `request_id`: unique within this connection for correlated messages.
- `method`: request method.
- `run_id`: optional run scope.
- `stream_id`, `offset`, `sequence`: where appropriate.
- `body`: method-specific JSON object.
- `error`: optional structured error object.

Metadata uses JSON; file chunks use raw payload bytes rather than base64. Exactly one writer owns stdout on each side. Readers dispatch frames without blocking on slow disk consumers. Worker stdout must never inherit the agent's protocol stdout.

### Handshake

The controller sends `hello` with supported protocol versions, expected node identity, controller build, and desired capabilities. The agent returns selected version, agent build, verified node identity, limits, capabilities, and an initial node snapshot.

Node identity must agree with both local AWS ownership and remote persisted identity. A new node identity can only be initialized by an explicit bootstrap operation after AWS verification. An existing mismatch fails closed.

The agent obtains a connection-scoped controller lease backed by an OS lock. A second managed controller receives `controller_busy`; interactive SSH shells are not protocol controllers. Losing the lease or connection does not affect worker execution.

### Methods

| Method | Behavior |
| --- | --- |
| `describe_node` | Hardware, storage capacity, active run, agent capabilities, deadlines. |
| `list_runs` | Paginated durable run summaries. |
| `get_run` | Authoritative run snapshot and latest event cursor. |
| `prepare_run` | Validate and persist an immutable manifest; report missing blobs. |
| `upload_blob` | Open or resume a bounded, checksummed upload stream. |
| `finalize_run` | Verify manifest/blob references and materialize the bundle atomically. |
| `start_run` | Reconcile or start the named run under systemd. |
| `subscribe` | Stream node/run events after a supplied cursor. |
| `stop_run` | Idempotent graceful stop of the named run only. |
| `get_artifacts` | List finalized artifact names, sizes, and digests. |
| `download_artifact` | Stream one declared artifact, optionally from an offset. |
| `delete_run` | Explicitly delete an inactive run and its artifacts. |
| `gc_blobs` | Remove unreferenced immutable blobs under an exclusive maintenance lock. |

The protocol exposes typed operations, not arbitrary shell commands or arbitrary filesystem paths. AWS destruction remains a controller AWS operation.

### Requests, idempotency, and cancellation

Request IDs correlate messages but are not durable idempotency keys. Run ID plus immutable manifest digest identifies preparation/start operations. Repeating `prepare_run` with identical inputs succeeds; conflicting inputs return `manifest_conflict`.

`start_run` must reconcile durable worker status, the systemd unit, and the exclusive start marker before submission. Persist intent before invoking systemd. A lost response never authorizes a new ingestion. If the unit was submitted but has not published status, report `starting`; do not submit another worker. Preserve the existing exclusive marker as the final protection against duplicate replay.

On reconnect, query authoritative state before retrying mutations. Do not blindly replay all pending requests.

`cancel` cancels a subscription or transfer request, not the benchmark. Cancellation of a start request after submission may leave the worker running; report or reconcile the outcome. Only `stop_run` stops remote execution. Closing the TUI or pressing local cancel must retain this distinction.

Errors include a stable code, human-readable message, retryability, and optional run/stage details. Initial codes include `node_busy`, `controller_busy`, `manifest_conflict`, `invalid_state`, `deadline_expired`, `unsupported_version`, `checksum_mismatch`, `insufficient_space`, and `not_found`.

## Events and reconnect

Workers persist structured events independently of the SSH agent. Agent subscriptions read/tail those journals rather than keeping the only copy in memory.

Events include:

- Phase and stage transitions: replay, flush, settle, restart, baseline, comparison, packaging.
- Replay progress and benchmark/version identity.
- Log records with source, timestamp, and severity when available.
- Settlement diagnostics: why the quiet window is blocked, last sample, elapsed time, and timeout.
- Artifact publication and terminal outcome.

Each run has an ordered monotonic cursor persisted across agent reconnects. Persist lifecycle events durably before exposing the associated transition. Batch ordinary log writes to avoid an fsync per line. Bound retention by bytes; retain terminal summaries and report an explicit cursor gap if older logs were evicted. A snapshot can always reconstruct current state without the full log history.

Subscription startup must not lose events between snapshot and tail: return a snapshot with its cursor, then stream strictly after that cursor. The controller persists processed cursors and tolerates duplicate delivery. Following a terminal run ends only after the artifact publication event or an explicit artifact-generation failure.

A reconnect uses exponential backoff with jitter and a bound, remains cancellable, and revalidates AWS ownership/address when necessary. SSH keepalives detect broken transport; protocol ping may be added for agent responsiveness but does not replace worker state reconciliation.

## Flow control and transfers

Use bounded per-stream queues and explicit byte credits for event and artifact streams. Producers cannot send more bulk data than the receiver has credited. Bound the number of concurrent requests and transfers; control requests retain reserved queue capacity.

Schedule control responses and stop requests ahead of bulk transfer frames. One already-written chunk cannot be preempted, which is why chunk size is bounded. Remote execution never waits for a log subscriber: slow consumers read from durable journals, and retention gaps are reported instead of accumulating unbounded memory.

Uploads use content-addressed SHA256 blobs. Validate size, available disk space, final digest, and run manifest references. Partial uploads are private and resumable only from a server-confirmed offset. Verify the complete digest before atomic publication. The first version may restart interrupted uploads if retaining partials would complicate recovery, but must expose that decision clearly.

Prefer uploading individual declared files instead of extracting arbitrary archives. Manifest paths must be relative, normalized, and reject traversal, symlinks, devices, and duplicate destinations. Executable permissions come from a constrained manifest field.

Downloads expose only declared artifacts of a run. The controller writes `.partial`, validates size and full SHA256, fsyncs, then atomically renames. Offset resumption requires matching artifact identity/digest. Never publish a partially transferred archive as complete.

Archive readiness is separate from worker measurement outcome: record execution outcome, finish cleanup/profiles, build and atomically publish diagnostics/archive, then publish terminal run state and artifact availability. If packaging fails, preserve execution outcome and record a separate artifact error; do not pretend results exist.

## Worker lifecycle and deadlines

Keep workers detached under systemd. Stopping requests graceful termination with the current bounded shutdown period, followed by forced process-tree cleanup if necessary. Before another run starts, verify the prior unit is inactive and its execution lock is released.

Reconcile abnormal outcomes:

- Marker exists, no live worker, no terminal status: `interrupted`.
- Terminal status exists: return it, even if systemd still retains the unit.
- Unit submitted but status not yet written: `starting`, with bounded startup diagnostics.
- Host reboot: interrupted runs are not automatically replayed.

Separate run execution deadlines from node allocation policy. A new run gets a fresh explicit deadline, capped by any configured node deadline. An expired node deadline requires explicit renewal before starting another run. Renewal must update persisted state/tags consistently and must not silently extend an already running worker's deadline.

Deadlines stop work; they do not delete AWS resources. Continue displaying the billing warning and support independent account-level cleanup.

## Reusing a node

The TUI's “new run on this node” action clones selected inputs or opens a new run form, resolves new immutable commits, builds required artifacts, uploads missing blobs, and starts under a new run ID after confirmation.

Initial policy:

1. Node must be owned, compatible, idle, and have sufficient disk space and remaining lifetime.
2. Run uses fresh storage and credentials; datasets replay once into this run only.
3. Old logs/results remain accessible until explicitly deleted.
4. Failed/stopped runs cannot be restarted in place.
5. Cached binaries can be reused by digest without rebuilding remotely.

Future query-only reuse must be a separate explicit feature: immutable dataset snapshot identity, ingest revision/storage compatibility, successful ingestion validation, exclusive dataset ownership, and clear cache semantics. It is not enabled by this rewrite.

## TUI changes

Present nodes with their runs, or separate node/run panes. Show node allocation and connection state independently from active run phase and last observed timestamp.

Actions:

- New node / provision: AWS lifecycle with confirmation.
- New run: choose a new node or an existing idle node.
- Clone/rerun: new run ID using prior inputs, with refs resolved according to explicit form choices.
- Attach: establish/reuse the managed connection and subscribe; no polling SSH subprocesses.
- Collect: transfer artifacts through that same connection.
- Stop run: explicit confirmation naming run and node.
- Delete run: explicit confirmation; inactive runs only.
- Destroy node: separate confirmation showing active run and uncollected results; force requires explicit acknowledgement.
- `h`: independent interactive SSH connection using existing ownership/address/key checks. Managed subscriptions and transfers remain active.
- Local cancel/detach/quit: never implicitly stop the worker or destroy the node.

Long-running transfers and subscriptions must not block TUI event handling. Route events through bounded controller channels. Multiple local views of the same node share the same connection rather than acquiring another lease.

## Security and ownership

- Retain AWS ownership verification before connect, stop/destroy, and address changes.
- Preserve host-key pinning per node across its runs; never silently replace a changed host key.
- Root control is limited to the disposable worker host. Run the agent with `sudo -n`; do not introduce a network listener or new credentials.
- Validate all run IDs, digests, lengths, offsets, enum values, and paths before use.
- Never accept arbitrary unit names, commands, or absolute download paths from protocol requests.
- Keep generated credentials, keys, and raw fixtures out of result manifests.
- Reject concurrent mutation by independent controllers, while retaining worker-side locking as defense in depth.
- Treat hashes as integrity identifiers, not as a replacement for SSH authentication or trusted local builds.

## Migration

Introduce a new local state version with explicit node/run records. Migrate existing sessions transactionally: each old provisioned session becomes one node and one associated run; drafts without resources become unassigned run drafts. Preserve AWS IDs, key names, tags, SSH identity, deadlines, errors, and results paths. Do not rename AWS resources as part of local migration.

Keep legacy remote runs readable/collectable through an agent compatibility adapter using their existing fixed paths and unit. Do not move directories or upgrade a live worker. A legacy active worker must count as occupying the node before any new run is permitted.

After a legacy run is terminal and its unit/processes are confirmed inactive, new runs use isolated directories. Preserve the legacy start marker and diagnostics; never clear it to enable reuse.

Fail closed on unknown state versions or ambiguous ownership. Migration must be restartable, keep the old snapshot until new records are durable, and avoid duplicate node records after interruption.

## Implementation plan

1. **Model and paths:** add node/run records, migration, `RunPaths`, and per-run systemd identities; preserve existing behavior behind tests.
2. **Codec and protocol:** implement bounded framing, handshake, typed requests/errors, client routing, and one-writer serialization with in-memory transport tests.
3. **Agent lifecycle:** implement durable run discovery, control lease, execution lock, idempotent prepare/start/stop, and legacy reconciliation.
4. **Bootstrap and connection manager:** one SSH process with same-stream agent installation, reconnect, cancellation, and ownership validation.
5. **Transfers:** content-addressed upload, manifest finalization, artifact streams, checksums, backpressure, and atomic publication.
6. **Events:** durable phase/progress/log stream, cursors, reconnect replay, settlement diagnostics, and bounded retention.
7. **TUI integration:** shared node connection, node/run views, new run on existing node, independent shell, distinct run stop/node destroy.
8. **Remove old managed transport:** retire SSH polling and SCP once lifecycle tests and live validation pass; retain SSH only for bootstrap/transport and interactive shells.

Each step should be a reviewable commit. Avoid leaving two production execution paths with different at-most-once guarantees.

## Tests and acceptance criteria

### Unit and protocol tests

- Partial reads/writes, oversized frames, malformed JSON, truncated payloads, unknown versions, and disconnect during any frame.
- Concurrent requests and interleaved streams cannot corrupt framing or misroute responses.
- Slow consumers keep memory bounded; stop/cancel remains responsive during transfers.
- Path traversal, invalid IDs, symlink manifests, digest mismatch, and disk exhaustion fail safely.
- Connection cancellation does not become `stop_run`.
- Duplicate starts and lost responses never trigger another replay.
- State migration is atomic and restartable.

### Integration tests

Use a local fake stdio transport for most tests and a disposable Linux/systemd environment for process ownership tests:

- Bootstrap fresh and cached agents without a second SSH/SCP connection.
- Start a run, kill the controller/agent, reconnect, and observe the same worker.
- Run baseline/comparison, collect results, then run a second benchmark on the same node with isolated storage.
- Failed, stopped, or interrupted run followed by a new run ID; old artifacts remain accessible.
- Concurrent start requests yield exactly one active worker.
- Reboot/worker crash produces `interrupted`, never automatic ingestion.
- Partial transfer never appears as a complete artifact.
- Interactive `h` opens a second connection while streaming remains healthy.
- Legacy active workers prevent new execution and remain collectable.

### Live acceptance

On a disposable EC2 node, verify only one managed SSH process is maintained throughout upload, execution, log/status streaming, and result download. Open `h` and verify exactly one additional interactive connection. Disconnect and reconnect during replay and artifact download. Complete two independent runs on the same instance, then collect both and explicitly destroy the node. Verify resource cleanup through AWS.

Benchmark measurement, CPU allocation, tenant isolation, and artifact provenance must remain equivalent to the existing harness, except for explicit new run isolation and richer progress reporting.
