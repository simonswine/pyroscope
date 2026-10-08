# TSDB-only compaction plan

## Goal

Build tenant-wide TSDB-only blocks on compaction workers at two time-aligned
levels:

- Hour blocks cover complete UTC hour slots.
- Day blocks cover complete UTC day slots.

These blocks contain no profiles or symbols. They are used by index-only read
queries (label names, label values, and series) and are built only for whole
tenant slots. The hour-level blocks are deleted after the corresponding day
block has been built.

## Existing behavior

- Each current block already includes a tenant-wide Format1
  `dataset_tsdb_index` dataset. It contains series labels and fingerprints,
  but no profile data or symbols.
- The query backend can execute index-only queries directly against a Format1
  dataset without resolving its concrete datasets.
- Regular compaction jobs are keyed by tenant, shard, and compaction level,
  and replace their explicit input blocks after completion.
- Block levels at or above the configured regular-compaction levels are not
  added to the normal compaction queue.

Relevant code:

- `pkg/block/compaction.go`
- `pkg/block/dataset_index.go`
- `pkg/compactionworker/worker.go`
- `pkg/metastore/compaction/compactor`
- `pkg/metastore/compaction_raft_handler.go`
- `pkg/querybackend/query.go`
- `pkg/frontend/readpath/queryfrontend/query_frontend.go`

## Design

### Dedicated TSDB block levels

Add constants for two levels outside the regular compaction-level range:

| Constant | Suggested value | Meaning |
| --- | --- | --- |
| `CompactionLevelTSDBHour` | `8` | Tenant-wide one-hour TSDB block |
| `CompactionLevelTSDBDay` | `9` | Tenant-wide one-day TSDB block |
| `TSDBIndexShard` | `math.MaxUint32` | Pseudo-shard for tenant-wide TSDB blocks |

Use distinct `__tenant_dataset__` label values:

- `tsdb_index_1h`
- `tsdb_index_1d`

The existing `dataset_tsdb_index` value continues to identify the per-block
Format1 input datasets. This naturally keeps TSDB-only blocks out of profile
queries, which request regular datasets or `dataset_tsdb_index` datasets.

### Slot rules

- Slots use UTC boundaries and Unix milliseconds.
- An hour slot is `[HH:00:00, HH+1:00:00)`.
- A day slot is `[00:00:00, next day 00:00:00)`.
- Build a block only after the entire slot is in the past plus its configured
  late-data delay.
- The block metadata uses `MinTime=slotStart` and `MaxTime=slotEnd-1`.
- Derive a deterministic ULID from tenant, TSDB level, and slot start. Its
  timestamp is the slot start, so retries are idempotent and the block is
  stored in the correct metastore partition.

### Block format

Each output object contains one anonymous Format1 dataset with only a TSDB
section:

```text
BlockMeta
  tenant: tenant ID
  shard: TSDBIndexShard
  compaction_level: TSDB hour or day level
  min_time/max_time: full slot boundaries
  datasets:
    format: DatasetFormat1
    name: anonymous
    labels: __tenant_dataset__=tsdb_index_1h|tsdb_index_1d
    table_of_contents: [index offset]
```

There is no profiles Parquet section and no SymDB section.

### Input selection and merge

TSDB jobs must identify their inputs by time slot, rather than taking an
explicit source-block snapshot. Regular compaction can replace source blocks
between planning and execution.

At execution, the worker queries the metastore for the tenant and slot:

- Hour job: all `__tenant_dataset__="dataset_tsdb_index"` datasets over the
  hour.
- Day job: all `__tenant_dataset__="tsdb_index_1h"` datasets over the day.
  For missing hours, resolve their regular `dataset_tsdb_index` inputs as a
  fallback, so a day block is still complete.

Open only `SectionDatasetIndex` / `SectionTSDB`. Iterate the all-series
postings, read the labels and fingerprint, deduplicate fingerprints across all
inputs, then write the union with `DatasetIndexWriter`.

The v1 implementation may hold the deduplication set in memory. If tenant
cardinality makes that too expensive, replace it with a sorted k-way merge in
a follow-up.

## API and metadata changes

Add slot fields to both job-plan representations:

- `api/metastore/v1/compactor.proto`: `CompactionJob`
- `api/metastore/v1/raft_log/raft_log.proto`: `CompactionJobPlan`

```proto
int64 slot_start_time = 7;
int64 slot_end_time = 8;
```

Regular jobs leave these fields at zero and retain explicit
`source_blocks`. TSDB jobs set the slot fields, use `TSDBIndexShard`, and have
no explicit source blocks when planned.

Run `make generate` and commit the generated API code.

## Implementation steps

### 1. Add block primitives

- Add TSDB compaction-level, pseudo-shard, and dataset-label constants in
  `pkg/block/metadata` or a focused TSDB-compaction package.
- Add `block.CompactTSDBIndex` in `pkg/block/tsdb_compaction.go`.
- Accept tenant, level, slot range, source block metadata, storage, and the
  existing object/temporary-directory options.
- Open only Format1 source datasets for the requested tenant.
- Merge and deduplicate their series labels and fingerprints.
- Write the single-dataset Format1 block with deterministic ID, metadata, and
  object path.
- Return no output metadata for an empty input set.

### 2. Add the TSDB planner

Create `pkg/metastore/compaction/tsdbcompactor`, implementing
`compaction.Planner` alongside the existing regular block planner.

- Change `Planner.NewPlan(*raft.Log)` to accept the Bolt transaction as well:
  `NewPlan(*bbolt.Tx, *raft.Log)`. The planner needs indexed tenants and their
  oldest data time.
- Wire that signature through the metastore compaction command handler.
- Maintain persistent planner state in a dedicated Bolt bucket:
  tenant/level cursor, in-flight slot, and retry state as needed.
- Discover tenants through `index.GetTenants(tx)` and process them in sorted
  order for deterministic Raft planning.
- For each tenant, schedule hour slots first and day slots second.
- Start backfill at the aligned maximum of the tenant's oldest profile time
  and `now - backfill-max-age`.
- Schedule at most one in-flight slot per tenant and TSDB level.
- Advance the cursor when a job is added. Clear the in-flight marker on
  completion. On eviction, make the slot eligible for retry.
- Limit TSDB jobs created per planner invocation so normal compaction work is
  not starved.

Invoke this planner only after the regular planner produces no work, preserving
the existing priority for profile-data compaction.

### 3. Add configuration

Add a nested metastore compaction configuration, registered under
`metastore.compaction.tsdb-index.`:

| Flag | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Enable TSDB-only compaction |
| `hour-delay` | `30m` | Grace period after an hour ends |
| `day-delay` | `1h` | Grace period after a day ends |
| `backfill-max-age` | `48h` | Maximum historical backfill window |
| `max-jobs-per-plan` | `2` | Maximum TSDB jobs per planning update |

Use the existing scheduler, job leases, retries, and worker capacity. Keep the
feature disabled by default for staged rollout.

### 4. Extend the compaction worker

In `pkg/compactionworker/worker.go`:

- Add metadata-query RPC access to `MetastoreClient`.
- Dispatch TSDB-level jobs to `runTSDBCompaction`.
- Resolve inputs at execution through `QueryMetadata`, as described above.
- Invoke `block.CompactTSDBIndex` with the worker's existing temporary
  directory and object download options.
- Do not create a sample observer for TSDB jobs.
- Report the new day block and its hour block source list on successful day
  compaction. Report no source blocks for an hour job.
- Reuse normal job duration/status metrics and add
  `tsdb_index_slot_lag_seconds` measured from slot end to completion.

`metadata-source=object-storage` remains applicable to regular jobs only:
TSDB jobs always resolve their time-slot inputs through the metastore.

### 5. Completion and deletion behavior

Update `pkg/metastore/compaction_raft_handler.go`:

- Do not create tombstones when `SourceBlocks.Blocks` is empty. Hour jobs are
  additive and must not delete regular profile blocks.
- A day job reports the matching hour blocks as source blocks, with
  `Shard=TSDBIndexShard`. Existing `Index.ReplaceBlocks` then inserts the day
  block, removes the hour blocks, and creates their object tombstones.
- Ensure TSDB blocks are not submitted to the regular compaction queue. The
  existing out-of-range-level behavior supports this; cover it with tests.

The metastore index and existing cleaner should store and retain pseudo-shard
blocks normally. Verify retention and shard tombstone behavior for the
pseudo-shard explicitly.

### 6. Use TSDB blocks on the read path

Add a feature flag, initially defaulting off:

`query-frontend.tsdb-index-blocks-enabled`

Also add a per-tenant override if staged tenant rollout is required.

In `QueryFrontend.QueryMetadata`:

1. Enable slot selection only when all requested query types are index-only:
   label names, label values, or series labels.
2. Split the requested range into complete aligned day slots, then complete
   aligned hour slots, then partial remainders.
3. Query matching day blocks first and mark their complete day slots covered.
4. Query hour blocks for uncovered complete hour slots and mark those slots
   covered.
5. Query ordinary `dataset_tsdb_index` blocks only for uncovered complete
   slots and partial remainders.
6. Concatenate the resulting metadata and use the existing query-plan and
   query-backend path unchanged.

Format1 dataset weights and the query backend's existing index-only handling
already model a TSDB-only block as one TSDB lookup. Profile queries,
ProfileTypes, and GetProfileStats remain unchanged.

## Tests

### Block package

- Build fixture Format1 blocks and compact them into a TSDB-only block.
- Verify the output has exactly one Format1 dataset and no profile or symbol
  sections.
- Verify label names, label values, and series equal the input union.
- Verify fingerprint deduplication across inputs.
- Verify empty input produces no block.
- Verify deterministic IDs and metadata time boundaries.

### Planner

- Slot eligibility around hour/day boundaries and late-data delays.
- Initial backfill cursor and configured maximum age.
- Tenant ordering and deterministic plan creation.
- One in-flight slot per tenant/level.
- Idempotent plan updates, completion, eviction, and Bolt-state restore.
- Regular work remains preferred over TSDB work.

### Worker and handler

- Hour job resolves only regular Format1 datasets for its full slot.
- Day job merges hour blocks and falls back to regular blocks for missing
  hours.
- Hour completion creates no tombstones.
- Day completion tombstones and removes hour blocks only.
- Source replacement during a job is retried safely.

### Read path and integration

- Range splitter selects day, hour, and regular fallback ranges correctly.
- Index-only queries return equivalent results with the feature flag on and
  off.
- Non-index-only queries never select TSDB-only blocks.
- End-to-end: ingest data, produce an hour block, then a day block; verify
  label and series queries and confirm hour block deletion after the day job.

## Rollout

1. Land protobuf, block writer, and tests.
2. Land worker and planner wiring with TSDB compaction disabled.
3. Validate produced blocks manually in a non-production environment.
4. Enable TSDB compaction for a limited deployment/tenant set.
5. Enable read-path selection behind its independent feature flag.
6. Monitor worker duration, slot lag, object-storage reads, query result
   parity, and metastore block counts before wider rollout.

## Open decisions

1. Confirm the level values (`8`/`9`) and `math.MaxUint32` pseudo-shard, or
   introduce an explicit job-kind enum instead.
2. Confirm that the configured late-data delay is sufficient. Late-arriving
   data after an hour block is built is not included unless a later day build
   reads regular blocks for all hours; the proposed day build does this only
   for hours missing an hour block.
3. Confirm whether per-tenant enablement is required for the compaction
   planner, rather than only for the read-path feature flag.
