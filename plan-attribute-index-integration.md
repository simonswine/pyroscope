# Attribute index integration plan

## Goal

Write a tenant-wide attribute index in segment writers and rebuild it during
normal compaction, alongside the existing tenant-wide TSDB index.

The attribute index must support both attribute discovery and the same
selector-to-dataset lookup as the tenant-wide TSDB index. Keep both TSDB indexes
(the per-dataset index and the tenant-wide index) and leave production query
routing unchanged initially.

## Decisions and scope

- Rename `pkg/attributeindex` to `pkg/attributeindex` and use **attribute index**
  and `AttributeIndexV1` terminology.
- Embed each tenant's attribute-index payload in `block.bin`, represented by a
  dedicated anonymous pseudo-dataset. Keep the payload's encoding independent
  from the containing block format.
- Use a new dataset format; do not overload `DatasetFormat1`, which means the
  tenant-wide TSDB index.
- Build indexes from complete persisted series labels, not incoming labels
  before ingestion normalization and profile-type expansion.
- Store mappings from matching attribute entities to real dataset positions in
  the containing block's `BlockMeta.Datasets`.
- Rebuild the attribute index from merged profile contents during compaction,
  just like the current tenant-wide TSDB index.
- Initially use legacy, string-valued attributes and coarse coverage semantics.
- Compress data pages independently with Zstd using
  `github.com/klauspost/compress/zstd` and `zstd.SpeedFastest`. This is the chosen
  codec and level, not an open codec-selection benchmark.
- Keep production reads on TSDB until lookup equivalence, query semantics, and
  performance are validated separately.

This revises the integration direction in `plan-attribute-block.md`, which
proposes separate `attributes.bin` objects and independently scheduled attribute
compaction. Those object lifecycle and scheduling changes are not needed here.

### Non-goals

- Removing either existing TSDB index.
- Independently scheduled attribute-only compaction.
- Directly merging input attribute-index pages during profile compaction.
- Exact per-series time/activity indexing.
- Native OTLP scoped/typed ingestion or new public query APIs.
- Switching production metadata or profile queries to the attribute index.

## Current implementation and integration points

### Segment writer

`pkg/segmentwriter/memdb/head.go` retains flushed series in
`FlushedHead.datasetIndexSeries` and exposes them through `WriteDatasetIndex`.
These labels already reflect the persisted profile series.

`pkg/segmentwriter/segment.go`:

- Flushes real datasets in tenant/service order.
- Feeds `block.DatasetIndexWriter` with each series and its dataset position.
- Emits the tenant-wide TSDB pseudo-dataset at tenant boundaries.
- Uploads the containing block before publishing metadata or recording it in
  the metadata DLQ.

### Compaction

`pkg/compactionworker/worker.go` delegates to `block.Compact`.

`pkg/block/compaction.go`:

- Skips anonymous input datasets in `PlanCompaction`.
- Compacts real datasets into per-tenant output blocks.
- Rebuilds the tenant-wide TSDB index from merged rows in
  `addRowToDatasetIndex`.
- Appends the index before final metadata encoding and block upload.

Most new compaction logic therefore belongs in `pkg/block`, not the worker's
scheduler or job protocol.

### Attribute-index prototype

`pkg/attributeindex` provides a writer, page-oriented encoding, ranged reader,
and attribute-query primitives, but no production write-path integration or
dataset mapping.

Important limitations for this integration:

- `AddEntity` copies and appends entities; it does not deduplicate them.
- `Bytes()` materializes the complete index and intermediate structures.
- Entity, dictionary, and postings data include monolithic pages with a 64 MiB
  page-size limit.
- Native exact activity is not supported by the writer.
- Entity IDs are local to an index and must not be confused with dataset IDs.

## Data model and lookup contract

### Entities and dataset references

An entity is a unique, complete canonical attribute set. Legacy labels become
`ScopeLegacy` string attributes, including internal persisted series labels
such as `__profile_type__`.

Store a separate mapping:

```text
entityID -> complete attribute set
entityID -> sorted, unique dataset IDs containing that entity
```

A dataset ID is a `uint32` position in the containing block's full
`BlockMeta.Datasets` array, matching the current tenant-wide TSDB index's use of
`ChunkMeta.SeriesIndex`. It is not a position in a filtered metadata response.

Multiple dataset references are supported for a single entity. Deduplicating
identical entities must union their references. Fingerprints may accelerate
lookup, but canonical full equality determines identity.

Dataset references are structural metadata, not synthetic attributes. They must
resolve to real profile datasets belonging to the same tenant, never another
index pseudo-dataset.

### Selector-to-dataset lookup

```text
selector
  -> intersect attribute predicates over complete entities
  -> matching entity IDs
  -> union referenced dataset IDs
  -> sorted, unique dataset IDs
```

Evaluate conjunctions before mapping entities to datasets. Two attributes on
different series in the same dataset must not satisfy a conjunction.

The initial supported legacy matcher semantics must match TSDB, including
negative matchers and the interaction between missing labels and empty values.
Attribute discovery and projection continue to operate on attribute entities;
dataset references do not appear as returned labels.

### Compaction identity

Dataset IDs and entity IDs are local to an immutable output. Rebuilding an
index must use output dataset positions, never copy source positions.

A future direct index merger would require an explicit mapping from
`(source block, source dataset ID)` to output dataset ID, plus entity and
dictionary remapping. That is outside this milestone.

## Work breakdown

### 1. Rename and reconcile the design

- [x] Rename `pkg/attributeblock` to `pkg/attributeindex`.
- [x] Update package declarations, imports, test names, comments, and errors.
- [x] Rename format terminology to `AttributeIndexV1`.
- [x] Update the active design documents to describe embedded payloads and
      normal profile-compaction rebuilding, linking to this plan.
- [x] Separate the mechanical rename from encoding changes. Decide explicitly
      how old prototype payloads without dataset mappings are recognized.
      Preserve magic/version for the rename alone; use a version or required
      feature mechanism for incompatible encoding changes.
- [x] Ensure readers never interpret a missing dataset mapping as a valid
      empty lookup result.

> **Compatibility update:** After this mechanical rename, the prototype wire
> magic intentionally changed from `ATTRBLK`/`ATTRFTR` to `ATTRIDX`/`ATTRIFT`.
> This is a breaking change: readers reject old standalone prototype payloads;
> they cannot be mistaken for mapping-capable attribute indexes. The payload
> version remains 1; mapping-capable payloads instead require the explicit
> dataset-mappings feature bit.

### 2. Add dataset mapping to the encoding and reader

- [x] Add independently readable entity-to-dataset mapping pages and directory
      descriptors, with sorted, unique references per entity.
- [x] Define and validate counts, entity coverage, reference encoding, offsets,
      checksums, and integer limits before allocation or conversion.
- [x] Extend writer input to associate entities with dataset references.
- [x] Deduplicate canonical entities and union their references.
- [x] Keep output deterministic for equivalent logical input, including input
      order changes.
- [x] Expose a selector-to-dataset reader API returning sorted, unique IDs.
- [x] Reuse candidate-entity evaluation rather than reconstructing every
      matching attribute set to perform dataset lookup.
- [ ] Validate block-relative references and tenant ownership in the block
      integration layer, where full block metadata is available.
- [x] Preserve existing attribute-discovery and projection behavior.

#### Current encoding status

`AttributeIndexV1` remains at wire version 1. The incompatible
entity-to-dataset mapping addition is identified by the required
`dataset-mappings` feature bit in both the header and footer, rather than a
version bump. A feature-enabled payload contains exactly one independently
checksummed dataset-mapping page. That page contains one non-empty,
delta-encoded, strictly sorted dataset-reference list per entity.

`Writer.AddEntityWithDatasets` is the mapping-aware input. At encoding time it
sorts entities by complete canonical attribute content, deduplicates equal
entities, and unions their dataset references; equivalent logical inputs
therefore receive deterministic local entity IDs and bytes. The original
`AddEntity` remains available for attribute-discovery-only prototype payloads:
it writes no mapping feature or page, and `DatasetIDs` returns
`ErrDatasetMappingsUnavailable` rather than treating that absence as an empty
lookup. New block integration must use the mapping-aware input.

Block-relative reference bounds, pseudo-dataset exclusion, and tenant ownership
remain deliberately deferred until block integration, where complete
`BlockMeta.Datasets` is available.

### 2a. Add per-page Zstd compression

The prototype currently writes uncompressed pages. Compression is required for
this integration, including the new dataset-mapping pages.

- [ ] Use the existing `github.com/klauspost/compress/zstd` dependency with
      `zstd.WithEncoderLevel(zstd.SpeedFastest)` and
      `zstd.WithEncoderConcurrency(1)`. Bound concurrency at the build/task level
      rather than multiplying it through internal codec workers.
- [ ] Encode each entity, dictionary, postings, forward-column, and dataset-
      mapping page as an independent Zstd frame. Do not compress the entire
      index as one stream or require another page to decompress a selected page.
- [ ] Keep the header, root directory, and footer uncompressed so opening the
      index and discovering page ranges remain inexpensive.
- [ ] Extend page descriptors with an explicit codec and decoded length. The
      existing offset and encoded length identify the on-object compressed range.
- [ ] Define CRC32 over the stored compressed page bytes and check it before
      decompression; validate the exact decoded length before decoding contents.
- [ ] Reject unknown codecs and invalid encoded/decoded lengths. Specify format
      version/feature compatibility explicitly; do not reinterpret old raw pages
      as Zstd. Newly written data pages use Zstd even when a small page expands.
- [ ] Validate declared decoded sizes before allocation, cap decoder memory and
      window size, and enforce an output bound during decompression rather than
      relying only on a post-decompression length check.
- [ ] Account for compressed buffers, decompressed pages, and decoder scratch
      space in reader memory budgets. Reserve decoded capacity before decoding
      and release reservations on errors, cancellation, and reader close.
- [ ] Reuse encoders/decoders with explicit ownership and bounded concurrency;
      do not create a new codec instance per page. Preserve buffer lifetimes for
      cached decoded data and views.
- [ ] Add tests for compressed round trips across all page kinds, incompressible
      and empty payloads where supported, corrupt/truncated frames, wrong decoded
      lengths, oversized frames/windows, unsupported codecs, cancellation, and
      memory-budget exhaustion.
- [ ] Verify selective queries still fetch only required encoded page ranges and
      do not repeatedly decompress a cached page within a query.
- [ ] Benchmark compression ratio, encode/decode CPU, allocations, peak memory,
      and ranged-read bytes using `SpeedFastest` against the uncompressed
      baseline. Codec choice remains Zstd; use results to tune page sizing and
      resource budgets, not to silently change the selected level.

### 3. Add block metadata and embedded reading support

Primary files:

- `pkg/block/dataset.go`
- `pkg/block/metadata/metadata_labels.go`
- `api/metastore/v1/types.proto`
- New attribute-index integration files under `pkg/block/`

Tasks:

- [x] Define a dedicated attribute-index dataset format and section/access API.
- [x] Define a distinct metadata marker,
      `__tenant_dataset__="attribute_index"`.
- [x] Add the anonymous pseudo-dataset metadata constructor containing tenant,
      tenant-specific time bounds, absolute payload offset, size, and marker.
      Segment-writer and compaction emission are deferred to steps 5 and 6.
- [x] Keep payload-internal offsets relative to the attribute-index payload.
- [x] Implement an offset-bounded range source for opening the payload within
      `block.bin`; reject out-of-bounds and overflowing ranges.
- [x] Avoid automatically fetching the whole attribute payload or containing
      block when a ranged read is intended.
- [x] Update `WeightOf` and related accounting: a single-entry table of contents
      must no longer be assumed to mean TSDB.
- [x] Audit format dispatch and query dataset selection so attribute-index
      pseudo-datasets are never accidentally opened as TSDB/profile datasets.
- [x] Test metadata encoding/decoding, metastore storage and filtering, and
      tenant isolation.
- [x] Update protobuf format documentation and run `make generate` when
      protobuf or configuration definitions change.

Embedding the payload preserves the existing single-object upload, metadata
publication, DLQ, and tombstone/deletion lifecycle. No separate attribute-object
publication protocol is required.

### 4. Build a shared series adapter and bounded writer

Provide a dataset-aware builder API along these lines:

```go
AddSeries(datasetID uint32, labels model.Labels) error
```

The exact exported names and package placement can follow existing conventions.
The same conversion and identity rules must be used by segment writing and
compaction.

- [ ] Convert complete persisted labels into legacy string attributes.
- [ ] Snapshot label data before callers reuse backing memory.
- [ ] Deduplicate using full canonical equality and accumulate references.
- [ ] Propagate validation/encoding errors and support context cancellation in
      long build and encoding loops.
- [ ] Define builder lifetime and cleanup on success and every error path.
- [ ] Enforce entity/reference limits before `uint32` conversion.
- [ ] Establish explicit build-memory and output-size limits.
- [ ] Benchmark realistic compacted-block cardinalities and implement paging,
      splitting, or spill support as needed before broad enablement.
- [ ] If splitting is required, define how multiple physical index fragments
      are represented and combined; never silently omit entities or mappings.
- [ ] Specify failure behavior for enabled index generation. Never publish an
      incomplete index as complete. If an optional skip mode is introduced,
      represent absence explicitly and retain TSDB fallback.

A `WriteTo` wrapper around the current `Bytes()` implementation alone is not a
bounded-memory writer and should not be presented as one.

### 5. Integrate segment-writer emission

Primary files:

- `pkg/segmentwriter/memdb/head.go`
- `pkg/segmentwriter/segment.go`
- `pkg/segmentwriter/segment_test.go`

Tasks:

- [ ] Expose flushed series through an error-returning visitor or equivalent
      API so both builders can consume the same labels without reparsing TSDB.
- [ ] Maintain an attribute-index builder alongside `DatasetIndexWriter` for
      the current tenant during `flushBlock`.
- [ ] Capture each real dataset's actual position before appending its metadata
      and feed that same position to both builders.
- [ ] Flush both tenant indexes at each tenant boundary and after the last head.
- [ ] Compute time bounds from the tenant's real datasets, without relying on
      pseudo-dataset insertion order.
- [ ] Preserve correct dataset IDs after earlier tenants' pseudo-datasets have
      been appended to `BlockMeta.Datasets`.
- [ ] Handle empty/skipped heads consistently and avoid empty pseudo-datasets
      unless an explicit empty-index contract requires them.
- [ ] Propagate builder/write errors and clean up all builders.
- [ ] Include attribute bytes in block size and upload accounting while leaving
      upload, registration, and DLQ ordering unchanged.

### 6. Integrate compaction rebuilding

Primary files:

- `pkg/block/compaction.go`
- `pkg/block/compaction_test.go`
- `pkg/compactionworker/worker_test.go`, where worker-level coverage is needed

Tasks:

- [ ] Add an attribute-index builder to each tenant `CompactionPlan`.
- [ ] Feed it from the merged output-series stream alongside the TSDB builder.
- [ ] Use output dataset positions, accounting for the actual metadata layout.
- [ ] Avoid repeating attribute conversion for every row of a series; use
      equality-safe run deduplication and reset run state at dataset boundaries.
- [ ] Do not copy the existing fingerprint-only run check as the attribute
      index's sole identity test.
- [ ] Write the attribute pseudo-dataset before final metadata encoding/upload.
- [ ] Continue skipping anonymous input indexes and rebuild from real contents.
- [ ] Support old-only, new-only, and mixed old/new inputs without reading input
      attribute indexes as a prerequisite.
- [ ] Preserve the existing compaction publication and deletion lifecycle.
- [ ] Clean up builders on dataset-open, merge, encoding, and upload failures.

### 7. Validate equivalence and compatibility

The central lookup acceptance criterion is:

> For the same tenant, containing block, and supported legacy selector, the
> attribute index and tenant-wide TSDB index return exactly the same dataset
> positions, resolving to the same real datasets in full block metadata.

Test layers:

- [ ] Encoding round trips and dataset-mapping corruption cases.
- [ ] Identical attribute sets mapped to multiple datasets.
- [ ] Reordered labels, duplicate input entities/references, empty values, and
      forced fingerprint collisions in the new builder's lookup mechanism.
- [ ] Conjunctions that must match one entity, not separate series in a dataset.
- [ ] Equality, regex, negative, and missing-label matcher equivalence.
- [ ] Unfiltered lookups and selectors that match nothing.
- [ ] Multi-tenant and multi-service segment flushes, including dataset offsets
      shifted by earlier tenants' pseudo-datasets.
- [ ] Entity contents compared with persisted real-dataset series labels.
- [ ] Old-only, new-only, and mixed-input compaction.
- [ ] Repeated compaction generations with remapped output dataset positions.
- [ ] Deterministic output across equivalent input orderings.
- [ ] Cross-tenant, out-of-range, and pseudo-dataset reference rejection.
- [ ] Cancellation, short writes/reads, index limits, and resource cleanup.
- [ ] Existing metadata and profile queries unchanged with both indexes present.
- [ ] Metadata publication/DLQ and compaction result metadata retain the new
      pseudo-dataset correctly.

Do not broaden this work into an unrelated TSDB identity refactor. If parity
checks reveal pre-existing TSDB behavior that is incorrect, document it and
isolate the fix rather than reproducing it in the attribute-index design.

### 8. Instrument, benchmark, and roll out

- [ ] Add build-duration, encoded-size, entity/reference-count, and failure
      instrumentation, following existing metric-label conventions without
      introducing unbounded attribute-value labels.
- [ ] Benchmark segment flush and compaction CPU, allocations, peak memory,
      output bytes, and duration with and without attribute-index generation.
- [ ] Benchmark dataset lookup latency, ranged GET count, and bytes fetched
      against the tenant-wide TSDB index.
- [ ] Include high-cardinality tenants, many datasets, repeated series, and
      larger compaction levels; profile build hot paths.
- [ ] Add configurable write enablement for segment writers and compaction,
      initially disabled, and regenerate configuration documentation.
- [ ] Deploy compatible readers/metadata handling and compaction workers before
      depending on index availability.
- [ ] Enable dual writing on a limited scope and inspect overhead and parity.
- [ ] Keep production query routing on TSDB during this milestone.

Older compaction workers skip anonymous input indexes and rebuild only the TSDB
index, so they can remove attribute-index availability from their outputs.
Mixed-version operation must not assume every block has an attribute index.
Rolling back write enablement must remain safe while TSDB reads are retained.

## Time semantics and later query cutover

Use `TimeLegacyCoarseCoverage` initially. The pseudo-dataset's time range is the
tenant's coverage in the containing block, not exact per-series activity.

Do not infer exact activity from series minimum/maximum times or TSDB dataset
references. Lookup parity within a selected block does not by itself prove
end-to-end metadata-query time equivalence.

A later query-cutover plan must cover:

- Partial-window and service-filtered query behavior.
- Selecting the correct index for the tenant and using full metadata to resolve
  dataset references.
- Mixed old/new blocks and explicit fallback when an index is absent or its
  format is unsupported.
- Errors for corruption rather than silently returning no matches.
- Metadata discovery/projection equivalence and end-to-end profile-query parity.
- Any activity/coverage format changes needed before independent compaction.

## Suggested PR sequence

1. **Rename and design alignment**: package terminology and revised integration
   direction, without incidental encoding changes.
2. **Dataset-aware format and builder**: mapping pages, per-page Zstd
   `SpeedFastest` compression with bounded decoding, canonical deduplication,
   lookup API, limits, and unit tests. Compression may be a separate dependent
   PR if needed to keep the format and resource-accounting changes reviewable.
3. **Block integration**: pseudo-dataset format, metadata marker, bounded ranged
   reading, accounting, and compatibility tests.
4. **Segment-writer integration**: dual-index generation behind configuration,
   multi-tenant tests, and flush benchmarks.
5. **Compaction integration**: output-reference rebuilding, mixed-input and
   repeated-generation tests, and compaction benchmarks.
6. **Rollout readiness**: parity suite, operational metrics, scale hardening,
   documented enablement/rollback, and measured acceptance of overhead.

## Definition of done

- [ ] `pkg/attributeindex` replaces the old package terminology.
- [ ] Enabled segment writers and compaction workers emit valid tenant-wide
      attribute indexes alongside unchanged TSDB indexes.
- [ ] Attribute indexes map selectors to the same real dataset positions as the
      tenant-wide TSDB index for supported legacy semantics.
- [ ] Compaction rebuilds correct output mappings for legacy and indexed inputs.
- [ ] Tenant isolation, metadata layout, and existing query behavior are tested.
- [ ] Data pages use independent Zstd `SpeedFastest` frames, with validated
      descriptors, bounded decompression, and selective ranged-read tests.
- [ ] Writer resource limits and failure behavior are explicit and scale-tested.
- [ ] Performance overhead is measured before broad enablement.
- [ ] Publication, rollback, and mixed-version behavior are documented.
