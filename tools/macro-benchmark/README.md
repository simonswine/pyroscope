# AWS macro benchmark (experimental)

A single terminal UI owns preparation, temporary AWS resources, remote execution,
artifact collection, and explicit cleanup. Run from `tools/macro-benchmark` with
Go 1.25, OpenSSH, and your AWS profile/SSO session configured. The remote
Ubuntu image must provide Python 3 for same-connection agent bootstrap:

```sh
go build -o macro-benchmark .
./macro-benchmark
```

The controller supports macOS and Linux. The disposable worker targets Linux
amd64 and uses systemd, not Docker or Kubernetes.

## TUI controls

| Key | Action |
| --- | --- |
| `n` | New run: select benchmarks, Git refs, repetitions and benchtime |
| `e` | Edit the selected draft |
| `j` / `k`, ↓ / ↑ | Select a session |
| `p` | Discover settings and prepare the selected draft (no AWS resource creation) |
| `r` | Resume/reconcile the selected session, or attach to its remote worker |
| `R` | Rerun a completed or benchmark-failed storage owner on its existing instance (no replay) |
| `c` | Cancel/detach the current **local** operation |
| `g` | Collect the results archive |
| `h` | Open an interactive SSH shell on the selected instance; `exit` returns to the TUI |
| `s` | Stop the remote worker, after confirmation; keep the instance |
| `d` | Destroy the instance, security group, and imported key pair, after confirmation |
| `a` | Archive a draft or destroyed session locally so it no longer appears in the list (after confirmation) |
| `q`, Ctrl-C | Quit, leaving remote work and AWS resources alone |

Launch, stop, and destroy actions display a confirmation popup; press uppercase `Y` to proceed or another key to cancel. Preparation
resolves the selected Git refs to immutable commits and builds in temporary detached
worktrees. The controller, benchmark clients and integration harness come from the
source checkout recorded when the session was created. Preparation does not launch
instances or create AWS resources. Use `r` again to launch a prepared
session. During attachment, `c` lets you select and work with another session.

The controller verifies AWS ownership and uses a single persistent, non-PTY SSH
connection per attached session. Initial SSH connection failures are retried
for up to ten minutes while the instance boots; host-key and bootstrap errors
fail immediately. A small Python helper installs the checksummed agent on that
connection; bundle upload, worker control, status and
artifact download then use its framed protocol. The interactive `h` shell opens
an independent SSH connection and does not stop the worker. Exiting the shell
(or using OpenSSH's `~.` escape) returns to the dashboard.

`R` creates a new child session on the owner's instance. It requires a remote
`ingest-ready.yaml`, owner-scoped MinIO credentials, and the original local bundle;
older runs without these records are rejected. The child builds new benchmark
clients from the current checkout and reuses the original product commits and
replay windows. If the original benchmark names no longer exist, choose
replacements explicitly. The child has its own results and archive. Use `r` to
reattach to a child. Child destruction is not supported: `d` refuses rather
than risking the owner's storage or AWS resources. Stop a running child with
`s`. Destroying an owner invalidates its children. Compaction may change stored
data between measurements, limiting comparability.

The TUI remains session-oriented: owner and child each have a separate snapshot. Existing sessions started using the former `/tmp/benchmark`
worker are **not migrated** by this version; do not use `r`, `s`, or `g` on those
sessions until a compatibility adapter is available. The removed public
`prepare`, `run`, and `keygen` commands are replaced by the TUI. The private
`agent --stdio` and `worker` commands are not user entry points.

## Persistent state and resumption

All generated **local** state lives in:

```text
$XDG_STATE_HOME/pyroscope-macro-benchmark/
# default: ~/.local/state/pyroscope-macro-benchmark/
  controller.lock
  controller.log
  sessions/<session-id>/state.json
  archived-sessions/<session-id>/state.json  # after explicit archive
  ssh/id_ed25519[.pub]
  bundles/<session-id>/...
  results/<session-id>/...
```

Relative `XDG_STATE_HOME` values are ignored per the XDG specification. The root
is private (0700); snapshots, logs, and private keys are owner-only. One controller
holds an OS lock, released automatically even if the controller is killed.
JSON snapshots are versioned, fsynced, and atomically renamed. Corrupt or unknown
versions fail closed rather than creating replacement state. Archiving moves only
the local session snapshot to `archived-sessions/`; it keeps bundles and results
and never stops or destroys AWS resources.

Reopen the TUI and select `r` to reconcile a saved checkpoint with AWS and the
worker. The list initially shows **saved**, not necessarily current, remote
status. AWS resource tags recover responses lost before their IDs were saved;
EC2's client token prevents duplicate instance launches. Ambiguous or unowned
resources are rejected. Destroy is also checkpointed and retryable.

On EC2, systemd owns the worker and all its subprocesses, independently of SSH.
Closing the TUI, losing connectivity, or killing the local controller does **not**
stop replay or measurement. Reattaching reads the existing per-run worker status over the persistent agent
connection rather than starting another replay. Completed results download to
a temporary name, are SHA256-verified and fsynced before being published locally.

A durable remote start marker prevents double ingestion. If the **remote worker
itself** crashes or the host reboots, it is not automatically replayed into a
partially populated tenant. Collect any available diagnostics and create a new
session. This is controller resumption, not mid-replay process checkpointing.
Preparation and interrupted bundle uploads can be retried before remote work
starts (an interrupted upload restarts from byte zero). Failed/stopped benchmark sessions are not reused for ingestion.

**Billing warning:** quitting, detaching, stopping, and completing a benchmark
all leave the EC2 instance and EBS volume allocated. The persisted deadline stops
the worker; it does **not** terminate EC2 or delete resources. Collect results
with `g`, then use `d` and verify cleanup. The `ExpiresAt` tag is metadata, not an
AWS TTL. Use an independent account-level cleanup policy for unattended safety.
Deleting the local state directory is not AWS cleanup and loses access/recovery
information. Existing checkout-local YAML runs are not automatically imported.

## Configuration and discovery

Controller defaults and dataset metadata are defined in `defaults.go`; no run
YAML is required by the TUI. Benchmarks self-register in Go. YAML is used only
for generated bundle/artifact manifests.

New runs select **all benchmarks** by default. Enter comma-separated benchmark
names to select a subset. Defaults are baseline `HEAD^`, comparison `HEAD`,
**5 repetitions per benchmark per version**, and **benchtime `5x`** (25 timed queries per benchmark per version, plus warmups). The ingest
ref follows the comparison ref unless overridden. Git refs must exist locally;
commit changes before selecting them (working-tree changes are not product refs).

Preparation derives the required datasets from the selection, assigns each a
unique tenant, and persists the resolved commits and tenant mapping in `plan.yaml`.
Each dataset is replayed once using the ingest revision. Baseline and comparison
query the same tenant and measured replay window. Repeated preparation uses the
persisted commits rather than resolving moving branches again.

Discovery is read-only:

- Region: `AWS_REGION`, then `AWS_DEFAULT_REGION`, then `aws configure get region`.
- SSH source: public IPv4 `/32` from `https://checkip.amazonaws.com`.
- AMI: newest public Canonical Ubuntu Server 26.04 amd64 HVM/EBS image, constrained
  to Canonical account `099720109477` (no fallback publisher or OS release).
- Network: available IPv4 subnet with an active internet-gateway route and an AZ
  offering `c6i.8xlarge`. Prefer an eligible default VPC; ambiguous VPCs fail.

Use standard AWS SDK credentials (`AWS_PROFILE`, SSO, environment, or role).
The TUI edits benchmark/ref selections, not network settings;
custom deployments require code defaults. Preparation needs EC2 DescribeImages,
DescribeVpcs, DescribeSubnets, DescribeRouteTables and
DescribeInstanceTypeOfferings. Provisioning/recovery also needs instance, key-pair
and security-group describe/create/delete permissions, ingress authorization,
termination and tagging. Account encryption policies may require KMS permissions.
Use a dedicated principal and the provided `deny-snapshots.json` policy. No new
snapshot, AMI, VPC, subnet, or AWS S3 bucket is created.

## Workloads and hardware

| Preset | Recording | Download size | Queries |
| --- | --- | --- | --- |
| checkoutservice | 1 hour | 29.88 MB | SelectSeries and merged flamegraph |
| full-tenant | 3,500 services, 5 minutes | 7.17 GB | LabelNames, LabelValues, Series |
| high-volume-service | 2 hours | 19.32 GB | Stacktraces: flamegraph, tree, DOT, pprof |

Public GCS URLs are pinned by generation and passed directly to profilecli on
EC2; no separate fixture download is performed. Catalog size and CRC32C are
informational, not validated by the replay. When supplied, SHA256 is verified
by profilecli. Local fixtures require a trusted SHA256. The original recording speed is preserved, so replay
may take hours. Replay checks require nonzero pushes and zero failures, followed
by flushing, settling and an observed quiet compaction window.

Query cases are Go benchmarks in `benchmark/<case-name>/*_bench_test.go`.
Each package registers its name and dataset in `register.go` using `init()`.
Add a blank import to `benchmark/benchmark.go` when adding a package so its
registration runs in the controller (Go does not auto-discover packages).
Preparation compiles selected benchmarks into standalone test binaries; there
is no YAML case loader. Required datasets are deduplicated automatically.

To run a case against an existing cluster, set `PYROSCOPE_URL`, `TENANT_ID`,
`START_MS`, `END_MS`, `LABEL_SELECTOR`, and (for profile queries) `PROFILE_TYPE`:

```bash
go test ./benchmark/series-total -run '^$' -bench . -benchtime=5x -count=5
```

Preparation builds `profilecli`, the controller and benchmark clients from the
current checkout. It builds three cluster binaries from the resolved ingest,
baseline and comparison commits (identical commits share a build). The current
integration harness is copied into each detached worktree so all revisions use
the same orchestration; its source is preserved in the bundle's `harness/` directory.
Incompatible revisions fail during compilation or startup rather than falling back.
A pinned MinIO release is downloaded and SHA256-verified. Bundle checksums are
checked before remote execution. The resolved plan, source commit/diff and
measurement inputs are retained with results.

The host is `c6i.8xlarge`: 16 physical cores, one thread per core, 64 GiB RAM.
An encrypted 500 GiB gp3 root volume uses 6,000 IOPS / 250 MiB/s and is deleted
on instance termination. The default lifetime budget is eight hours.

| CPU set | Workload | Soft Go memory limit |
| --- | --- | --- |
| 0–1 | Controller/observation | default |
| 2–3 | MinIO | 8 GiB |
| 4–6 | Ingestion, metastore and compaction cluster | 12 GiB |
| 7–14 | Query frontend and backends | 33 GiB |
| 15 | Replay, then query benchmark | 4 GiB |

The write cluster uses 2 segment writers, 3 metastores, one distributor and one
compaction worker. The query cluster uses one frontend and 3 backends connected
to that existing metastore. **Components within each cluster share one Go process,
CPU allocation and GC** and use integration defaults, not a production deployment. Memory limits are soft runtime hints, not cgroups.
MinIO uses loopback S3 with random per-run credentials, never AWS S3.

Before **each benchmark/version**, the query cluster and write cluster (including
all three metastores) start fresh. All five repetitions run on those same
processes using `-test.count=5 -test.benchtime=5x`. Baseline and comparison are
paired per benchmark, not per repetition. The write cluster uses the ingest ref
and retains its WAL, snapshots and port assignments across restarts; datasets are
not replayed. MinIO stays alive. This resets process caches, not OS or object-store
caches, and allows caches to warm across repetitions. A successful warmup request precedes the timed loop;
all responses must be nonempty. This does not prove fixture completeness.

Both cluster processes write runtime CPU and heap profiles to disk, covering all
five repetitions together. CPU
profiling runs from readiness through the benchmark (including warmup and idle
time), stopping before teardown. The heap profile is written after GC and before
components stop; it includes process-lifetime allocation samples. `cpu.pprof` and `heap.pprof` cover the **shared frontend/backend runtime**.
`ingest-cpu.pprof` and `ingest-heap.pprof` cover the **shared write/metastore runtime**
(including recovery settling). They do not profile the HTTP benchmark client. Very short queries may still produce few or no CPU samples, even with `5x`.

To generate a comparison report from a collected archive:

```bash
go run . report ~/.local/state/pyroscope-macro-benchmark/results/<session-id>/results.tar.gz index.html
python3 -m http.server # open http://localhost:8000/
```

The header shows the baseline/comparison refs and commits from `plan.yaml`,
host details from `cpu.txt` (instance type and provisioned memory reflect the
current `c6i.8xlarge` runner), and the worker's UTC start, finish, and elapsed
time. New archives record `started_at` in `status.yaml`; older archives infer
the start from the first UTC `run.log` entry. This measures worker execution,
including dataset replay and cleanup, not EC2 provisioning.

The overview has one row per reported benchmark or sub-benchmark, initially
comparing `time/op`. Its metric selector switches the whole table to other
reported metrics
(benchstat means and significance) or query/ingest sampled CPU time (SI time
units) and sampled process-lifetime allocated bytes (binary memory units,
descriptive changes only). Profile captures are for the whole benchmark and
are never attributed to individual sub-benchmarks.
Missing measurements show as unavailable. Overview column headers sort by the
unscaled values (durations in nanoseconds, allocated bytes in bytes, and
changes in percentage points), with unavailable values last. Benchmark
statistics stay visible; only the flamegraph is collapsible, and profile JSON
loads when it is expanded.
The layout and flamegraph use the available viewport width.

Keep `index.html` and `index.assets/` together when publishing. Profile JSON files in
`index.assets/` are fetched only when a flamegraph is opened; browsers block
`fetch()` from `file://`, so serve the directory over HTTP (including GitHub Pages).

The report uses vendored benchstat for query results and the standalone flamegraph from `simonswine/grafana-flamegraph` for baseline, comparison, and diff profiles. See `report-ui/solo/VENDORED.md` for the pinned source commit. Each benchmark has a collapsible flamegraph with a profile picker; the viewer itself offers baseline, comparison, and diff controls. CPU profiles include warmup and idle; allocation flamegraphs use `alloc_space` from heap profiles captured after GC and include all allocations since process start. Differences are descriptive, not statistical tests of profile samples. To rebuild the embedded assets from `tools/macro-benchmark`, run `cd report-ui && corepack yarn install --immutable && corepack yarn build` (requires Vite installed in `../../../ui`). Commit the updated `report_assets/` alongside the source.

Results are isolated by version and benchmark (`bench.txt` contains all repetitions):

```text
baseline/<benchmark>/{bench.txt,cluster.log,cpu.pprof,heap.pprof,endpoints.yaml,metrics/}
baseline/<benchmark>/{ingest.log,ingest-cpu.pprof,ingest-heap.pprof,ingest-endpoints.yaml}
comparison/<benchmark>/...
plan.yaml
windows.yaml
replay-<dataset>.log
```

Query shutdown is awaited, then the write cluster is stopped, so all profiles
finish writing before the next benchmark/version starts. Results also include replay logs, metrics, host statistics, status and
provenance.
Use sanitized fixtures: application logs and source diffs can contain sensitive
data. Private keys, generated S3 credentials and raw fixtures are not included
in the results archive. SSH uses per-session known_hosts and trust on first use.

## Validation

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build .
```

Tests cover atomic state, exclusive controller locking, checkpoint recovery,
resource ownership/ambiguity, cancellation, lost worker-submission responses,
and at-most-once replay. A pseudo-terminal smoke test verified creating a draft,
quitting, and reopening it. The detached AWS lifecycle and new multi-revision
workflow have not yet been validated end to end on a live instance. Controlled
cold-cache tests, concurrent workloads and latency percentiles remain follow-up work.
