# Rerun benchmarks on an existing instance and replay

Status: partial implementation. New owners persist credentials and ingestion readiness; the controller, agent, worker, and TUI have a rerun path, but no EC2 end-to-end validation has been performed. Legacy runs without the required records remain ineligible. Child deletion and legacy recovery are not implemented. This document describes the target behavior, not a claim that all acceptance criteria are met.

## Goal and scope

Add a TUI action (`R`: rerun) for a **completed** session or one that **failed after ingestion and settling**. It creates a new, independently tracked run on the same EC2 instance, reuses its object-store and metastore data, builds fresh benchmark clients from the current checkout, and measures against the original tenant IDs and replay time windows. It must never push profiles again. Both baseline and comparison are measured, with fresh write/query processes, profiles, logs, and result archives. The original session and results remain untouched.

A rerun is not a retry of the old worker. Interrupted, stopped, failed-during-replay, destroyed, or still-running sources are ineligible; failures before the storage reached the benchmark phase are ineligible. A failed *rerun* can itself be rerun only if the original storage owner remains eligible. This feature is intentionally limited to one storage owner per instance; it does not provision a new instance, change datasets, or resume a partially completed benchmark in place.

## Existing behavior to preserve

- `worker.go` creates a permanent `started` marker. Never remove it or restart the same run ID. `agent_start.go` already provides durable start intent, node-busy checks and a per-run systemd unit.
- `execute.go` starts MinIO with random credentials, replays each dataset, waits for compaction to settle, stops the initial ingest process, then runs comparisons. On exit it stops MinIO and writes an immutable archive. `execute_restart.go` starts a fresh ingest process (including the metastores) for each benchmark/version, against persisted state; `execute_plan.go` starts a fresh query process. Those processes must be restarted for a rerun, not reused from old endpoints/PIDs.
- `run_paths.go` currently puts bundle, results and storage (`work/ingest`, `work/minio`) under the same run ID. `replayDatasets` writes `results/windows.yaml` incrementally, **before** settling; its presence alone is not evidence of a completed ingestion.
- `plan.go` generates new tenant IDs for new plans. Do not call that path when constructing rerun datasets. `prepare.go` rebuilds the whole bundle and verifies its checksums; do not use its normal replay preparation unchanged.
- `sessionHost` verifies EC2 tags using the original run ID; a child rerun cannot claim ownership of the parent's AWS resources. `resumeAWS` provisions resources and must not be called for a rerun. An `r` on a terminal session currently collects results, not reruns.

## User flow and configuration

1. Select an eligible source in the TUI and press `R`. Show source run ID, dataset/tenant list, old and new benchmark selections, baseline/comparison/ingest commits, instance ID, and a clear **no replay** confirmation. Reject if the parent or any other run on this node is active, or if the parent is being destroyed.
2. Create a **new** run ID and session snapshot, with `StorageRunID`/`ParentRunID` pointing at the source (or its original storage owner). Save the parent link before any remote work. Use the source's resolved product commits and ingestion revision, dataset definitions, tenant IDs, selector, profile type, benchtime, repetition count, and other measurement settings. Set a **new** deadline; never inherit the expired original deadline. Pin revisions by commit rather than re-resolving `HEAD` or requiring old Git refs to still exist.
3. Default to the original benchmark selection if every name still exists. If names were removed/renamed (e.g. `all-series` became `series`), require the user to choose replacements explicitly; never silently substitute or widen to all benchmarks. Permit only registered benchmarks whose datasets were fully replayed by the source; reject new datasets. Show any change from the source selection before confirmation. New benchmark test binaries and controller/agent code come from the current checkout; keep cluster product commits and original integration harness fixed for reproducibility. Record both source and current checkout provenance in results.
4. Reprepare into `bundles/<new-id>`, upload to `runs/<new-id>/bundle`, and run through the existing idempotent upload/start/poll/download machinery. Keep `results/<new-id>/` and worker markers separate. `r` on the new session attaches/collects it; `R` creates another new session. No second AWS resource owner is created.

The original session's result archive can have been downloaded already; remote persisted storage and a reachable original EC2 instance are required. Remote `runs/<source-id>/work/{minio,ingest}` must exist. The new run records `storage_source_run_id`, original plan/window digests, selected benchmarks and binary checksums in its own bundle and archive. Never copy credentials, private keys, temporary endpoints, PIDs, or source results into the new run as executable inputs.

## Eligibility and durability

Add a durable, fsynced `ingest-ready` manifest under the storage owner's run directory, written only after **all** replay summaries pass (`pushed > 0`, `failed = 0`), settling succeeds, the initial ingest process has stopped cleanly, and the final `windows.yaml` is persisted. Include the owner ID, dataset-to-tenant mapping, windows, ingest commit and checksums of the plan/window inputs. Publish atomically; absence or mismatch rejects a new-format rerun. Keep this marker separate from `status.json`: a benchmark can fail later without invalidating ingestion.

For runs made before this marker exists (including the motivating failed run), support a *conservative legacy check*: validate the remote source bundle and plan, all replay completion logs and all window entries, and evidence that the run reached `runBenchmarkGroup` **after** settling (for example a produced `baseline/<benchmark>/ingest-endpoints.yaml`). Require stopped source worker and no active cluster processes. If this cannot be proved, reject with a diagnostic; do not add an override that risks measuring partial ingestion. Do not infer readiness merely from `windows.yaml`, a completed worker status, or an archive that was copied locally. Make legacy validation explicit and testable; once validated, publish a durable readiness record for future reruns.

Before start, validate the remote storage paths via `Lstat`/canonical path checks: they must be owned regular directories underneath the expected source run root, with no symlink escapes. Verify the new bundle names the same owner, tenants, ingest commit and windows as the readiness record. Do not trust a client-supplied arbitrary path or overwrite a source run directory. Recheck eligibility under the existing node execution lock immediately before opening storage; preparation-time validation alone is insufficient. Do not start if another worker is running or pending on the node.

## Controller, agent and lifecycle

- Add an explicit rerun session kind and immutable parent/storage-owner ID to `sessionState` (version/migration policy for existing snapshots). Store the new ID and chosen benchmark list atomically before preparing or uploading. A separate preparation path reads the original plan and remote readiness record instead of generating tenants, and creates a checksummed rerun manifest within the normal checksummed bundle. The agent validates the owner ID and storage binding rather than accepting remote paths from a benchmark client.
- Rerun sessions use the parent's verified EC2 instance, region, SSH key and known host. `sessionHost` must check the **owner** run's tags, not the child ID; do not invoke `resumeAWS` provisioning for children or create/delete an EC2 key pair/security group on a child. `agentForSession` still boots a current compatible agent over SSH and validates the target run ID in its handshake. Ensure a controller restart can reconstruct the same relationship from saved state alone.
- Reuse `start_run` with a new run ID and distinct systemd unit, plus a versioned rerun manifest and explicit worker dispatch mode; reject unknown modes. Preserve permanent started/start-intent markers, checksum verification and artifact idempotency. Controller cancellation only detaches; worker cancellation/deadline stops *that rerun* and leaves the storage owner intact.
- `d` on a child deletes only its local/remote run artifacts if supported, **never** the parent's AWS resources or shared storage. `d` on the owner warns that all child reruns will lose their storage and terminates the shared instance only after confirmation. Disallow new reruns while owner destruction is pending. Distinguish owner and child in TUI listing and billing messages. Avoid deleting source runs as part of generic cleanup or archive operations.

## Worker execution

Factor common run setup/cleanup/metrics/archive behavior from `executeWithPaths`, but use separate `executeFresh` (replay + readiness publication + comparisons) and `executeRerun` (readiness validation + comparisons) entry points. Never call `replayDatasets`, `profilecli replay push`, or the initial ingest bootstrap in rerun mode. Rerun `RunPaths` must separate **storage paths** (`owner/work/minio`, `owner/work/ingest`) from **result paths** (`child/results`, `child/bundle`, `child/work/cluster`). Keep all generated endpoints, temporary files and profiles inside the child; no truncation of source results or old manifests.

Start MinIO on its existing data directory with fresh per-worker root credentials, then start the same per-benchmark ingest/metastore and query processes as `runBenchmarkGroup`/`runBenchmarkCluster`, with `MINIO_*` and `TMPDIR` set for the child. Validate bucket availability and persisted metastore readiness before measurement. Preserve original MinIO binary and ingest cluster binary/storage format; query cluster binaries use the pinned baseline/comparison commits. Respect CPU pinning, memory hints, timeouts, settle behavior, stop order and profiling from the normal run. Do not copy/reinitialize/clear metastore or MinIO state or re-create tenants. If restoring the original storage requires credentials or process configuration not currently persisted, add a safe per-owner persisted secret/config with restrictive permissions for **future** runs and a verified legacy recovery path; do not assume the archive contains those secrets.

Rerun executions use original `replayWindow` start/end and tenant for each selected dataset. Create a new `windows.yaml` in child results from verified source data. Call `runComparisons` unchanged as far as possible. A failed sub-benchmark fails the child, archives its diagnostics and does not modify source status. Source data may evolve due to compaction during measurement, as in the normal baseline/comparison sequence; document this comparability limit.

## Verification and acceptance criteria

- Unit tests: eligible completed and benchmark-failed sources; missing/incomplete replay; failure before settling; legacy eligibility; absent/corrupt storage or windows; wrong tenant/ingest commit; new dataset or missing benchmark; tampered bundle/manifest; symlink escape; expired old deadline vs new deadline; duplicate start, concurrent start, restart after controller cancellation.
- Lifecycle tests: child attach/resume/download/stop, ownership tags bound to parent, child destroy cannot terminate instance, parent destroy warning, independent result paths and status. Agent/worker tests must prove rerun mode cannot invoke replay and cannot start without a verified readiness record.
- Integration test using a small fixture: run ingest once, deliberately fail a query benchmark, rerun a newly compiled benchmark against the same owner on the same node, verify unchanged tenant IDs/windows and push counts, valid baseline/comparison artifacts and separate archives; retry attachment after disconnect; ensure no replay invocation or source artifact modification. Also test a completed parent.
- Acceptance: TUI `R` on the previously failed, query-stage session builds the new `series` benchmark, runs its sub-benchmarks on the original `full-tenant` replay without downloading/pushing that replay, and produces a new report while the old report and session remain available.

## Open decisions and invariants

Resolve these before implementation; default to rejection when evidence or ownership is ambiguous.

- **Storage credentials:** the current MinIO credentials are random and may not be recoverable after the original worker exits. Determine whether they can be safely persisted for new runs and whether legacy runs have a verifiable recovery path. If not, mark those sources ineligible; never guess credentials or reinitialize the data directory.
- **Owner lifecycle:** keep the original instance and volume as the sole storage owner. Child runs must not independently provision, extend, or destroy AWS resources. Define how a child deadline is bounded by the owner's actual availability without inheriting an expired session deadline.
- **Format compatibility:** verify that the preserved MinIO data, metastore, and ingest binaries can be reopened by the selected execution. A mismatch is an eligibility failure, not permission to migrate or rewrite shared storage during measurement.
- **Evidence and migration:** define canonical manifest serialization and digest inputs, fsync/rename durability, session snapshot migration, and precise legacy log/artifact evidence. Any absent, corrupt, conflicting, or unverifiable evidence rejects the rerun with an actionable diagnostic.
- **Concurrency and deletion:** the node lock must cover the final owner/readiness/path checks and opening storage. Destruction of an owner must account for all children and be serialized against rerun start; generic cleanup must never infer shared-resource ownership from a child.
- **Comparability:** record that compaction or other permitted storage evolution between measurements can affect results. Do not promise bit-for-bit equivalence to the original run.

## Implementation order

1. Readiness record, credential/recovery feasibility, and tests for new/legacy owner runs; canonical path and manifest validation.
2. Separate storage-owner paths from child output paths; rerun-only worker execution and integration test proving replay is unreachable.
3. Child session persistence and agent/controller lifecycle; ownership, concurrency, cancellation, and destruction semantics.
4. TUI confirmation/selection, documentation, and end-to-end validation on completed and benchmark-failed sessions.

Each stage must preserve existing fresh-run behavior. Do not expose the TUI action until worker-side validation, durable ownership state, and child-safe cleanup are in place.
