# Attribute index design

## Purpose and scope

The attribute index is an immutable, page-oriented index for attribute discovery,
series projection, and selector-to-dataset lookup. It avoids reading profile and
symbol data for metadata queries and supports selective object-store range reads.

The implementation lives in `pkg/attributeindex` and uses the `AttributeIndexV1`
format. Each tenant's payload is embedded in `block.bin` as an anonymous
pseudo-dataset alongside the existing tenant-wide TSDB index. Per-dataset TSDB
indexes remain unchanged.

Attribute indexes share the containing block's upload, metadata publication,
DLQ, and deletion lifecycle. Normal profile compaction rebuilds them from merged
profile contents. Separate `attributes.bin` objects, independently scheduled
attribute compaction, and direct merging of encoded index pages are not part of
this design.

## Logical model

An attribute key is `(scope, name)`. Scope is part of identity, not a name prefix.
The model preserves value types rather than coercing values to strings. Initial
block integration uses only `ScopeLegacy` string attributes derived from complete
persisted series labels, including internal labels such as `__profile_type__`.
Native OTLP ingestion and new public typed query APIs are outside this scope.

An entity is a unique, complete canonical attribute set. Full equality determines
identity; fingerprints may accelerate lookup but cannot replace equality checks.
Scope does not imply entity granularity: sample-level conjunctions cannot be
represented by flattening different samples onto one series entity.

```text
entityID -> complete attribute set
entityID -> sorted, unique dataset references
```

Entity IDs are dense and local to an immutable index. A dataset reference is a
`uint32` position in the containing block's full `BlockMeta.Datasets` array, not
in a filtered metadata response. References must identify real profile datasets
belonging to the same tenant, never index pseudo-datasets. Equal entities union
their dataset references. Dataset references are structural metadata and never
appear as returned labels.

Selector lookup evaluates all predicates against the same entity before mapping
results to datasets:

```text
selector -> matching entity IDs -> union of references -> sorted unique dataset IDs
```

Two different series in one dataset must not jointly satisfy a conjunction.
Legacy matcher semantics match TSDB, including regex, negative matchers, absent
labels, and explicit empty values.

## Payload format

The payload contains a header, scoped attribute directory, typed dictionaries,
presence/value postings, forward columns, entity data, dataset mappings, a root
directory, and a fixed-size footer. Internal offsets are relative to the payload,
not to `block.bin`. Forward columns support projection by attribute and entity
range; postings support filtering without reconstructing complete entities.

Wire magic is `ATTRIDX`/`ATTRIFT`, with version 1. Old standalone prototype magic
(`ATTRBLK`/`ATTRFTR`) is rejected. Dataset-aware payloads require the
`dataset-mappings` feature bit in both header and footer. Their mapping page has
one non-empty, delta-encoded, strictly sorted reference list per entity.

`Writer.AddEntityWithDatasets` canonicalizes entities, deduplicates them, unions
references, and produces deterministic bytes for equivalent logical inputs.
Discovery-only payloads written with `AddEntity` have no mapping feature;
`DatasetIDs` returns `ErrDatasetMappingsUnavailable`, not an empty lookup.

Readers validate feature flags, counts, offsets, lengths, checksums, entity
coverage, and integer bounds before allocation or access. The block integration
layer is responsible for validating references against full block metadata.

### Compression contract

Per-page compression is a format requirement to complete before broad rollout,
not a claim that the current raw-page encoder already implements it:

- Use independent Zstd frames with `zstd.SpeedFastest` and encoder concurrency 1.
- Compress data pages, including mappings; keep header, root directory, and
  footer uncompressed.
- Record explicit codec, encoded length, and decoded length in page descriptors.
- Check CRC32 over stored bytes before decompression and validate exact decoded
  length. Reject unknown codecs and incompatible features explicitly.
- Bound decoded output, decoder memory, and window size before decoding; a
  post-decompression length check alone is insufficient.
- Account for encoded buffers, decoded pages, and decoder scratch space. Reuse
  codecs with explicit ownership and bounded concurrency.

Page sizing and further paging/splitting require representative benchmarks.
Monolithic pages and whole-output materialization remain scalability constraints.

## Write path

`attributeindex.SeriesBuilder` is the shared dataset-aware label adapter for
segment writing and compaction. It accepts `model.LabelSet`, snapshots reused
label storage, canonicalizes full entities, and accumulates dataset references.
`BuilderLimits` bounds retained entity/reference data and output size.
`AddSeriesContext` and `Writer.BytesContext` support cancellation; `Close`
releases retained state.

The encoder currently materializes primitive pages and the complete payload.
Wrapping `Bytes()` in `WriteTo` would not make it a bounded-memory streaming
writer. Paging, splitting, or spill support must be driven by scale testing;
limits must fail explicitly rather than silently omit data.

### Segment writer

`FlushedHead.VisitDatasetIndexSeries` exposes complete persisted labels after
ingestion normalization and profile-type expansion. `pkg/segmentwriter/segment.go`
feeds the TSDB and attribute builders from this same stream.

Each real dataset's full metadata position is captured before appending index
pseudo-datasets. At tenant boundaries, both indexes are written using time bounds
accumulated from real datasets. Empty heads do not generate index entities, and
an empty builder emits no attribute pseudo-dataset. Attribute bytes enter the
existing block buffer before upload and metadata publication.

The dedicated attribute format uses the metadata marker
`__tenant_dataset__="attribute_index"`; it does not overload `DatasetFormat1`,
which denotes the tenant-wide TSDB index. Format dispatch and accounting must
not mistake an attribute payload for TSDB or profile sections.

### Compaction

`pkg/block/compaction.go` skips anonymous input indexes and rebuilds both tenant
indexes from merged real-dataset output. Attribute references use output metadata
positions, never source positions. Run deduplication compares full labels and
resets at output dataset boundaries.

The attribute payload is appended before final metadata encoding and upload.
Builder/encoding failures abort publication rather than publish an incomplete
index. Builders are closed on all exits. Old-only and mixed inputs are valid
because compaction does not depend on reading input attribute indexes.

Older workers may rebuild only TSDB and remove attribute-index availability from
outputs. Readers must therefore tolerate mixed availability across generations.

## Read path and resource ownership

An offset-bounded range source opens the payload inside `block.bin`, rejecting
out-of-bounds or overflowing reads without automatically fetching the whole block.

```text
required pages -> sorted/coalesced ranges -> ranged GETs
               -> owned buffers -> bounded decoding -> results
```

Bound request concurrency, encoded bytes in flight, decoded/cache memory, and
result size separately. Coalesce nearby ranges when cheaper than extra requests.
Propagate cancellation, close response bodies, detect short reads, and release
reservations on errors and reader close. Buffers remain alive while views refer
to them; returned values must own any data that outlives those buffers.

Cache identity must include tenant, immutable object identity, and page identity.
Do not issue one object-store request per value or silently truncate large results.

## Query routing and legacy adapters

Default routing remains TSDB. Experimental requests can opt in using
`X-Pyroscope-Use-Attribute-Index: true` (also accepting `1` and case-insensitive
`true`), propagated through `InvokeOptions.UseAttributeIndex`.

Choose exactly one tenant-wide index per tenant within each block. Prefer an
available attribute index under opt-in; fall back to tenant-wide TSDB only when
it is absent. Empty attribute results are authoritative. Corruption, I/O,
cancellation, and resource-limit errors propagate rather than trigger fallback.
Unsupported-format fallback would require an explicit capability-error contract.

| Planned request | Execution |
| --- | --- |
| Opt-in absent/false | Existing TSDB route |
| Opt-in index-only metadata, attribute index present | Direct attribute query |
| Opt-in index-only metadata, attribute index absent | Direct tenant-wide TSDB query |
| Explicit real datasets, including strict service equality | Existing per-dataset TSDB route |
| Profile or mixed metadata/profile request | Selector-to-dataset lookup, then real-dataset handlers |

Direct attribute execution supports `QUERY_LABEL_NAMES`, `QUERY_LABEL_VALUES`,
and `QUERY_SERIES_LABELS`. It opens only `SectionAttributeIndex` and must not
call `DatasetIDs`, resolve real datasets, or fetch full metadata merely for
discovery. An explicit query allowlist prevents attribute bytes reaching
TSDB-only handlers. Existing report aggregation remains unchanged.

Legacy adapters preserve these contracts:

- Names expose only legacy keys, sorted and deduplicated.
- Values preserve explicit empty strings for unfiltered discovery; filtered
  discovery omits them, matching TSDB. Missing labels are not synthetic values.
- Series apply selectors before projection, convert only legacy string values,
  omit empty-valued labels, and deduplicate after normalization with full equality.
- No requested names means full projection. Requested names with no intersection
  with available labels produce no series. An intersecting projection may still
  yield an empty label set after normalization.
- Internal persisted labels remain visible; typed values are never stringified
  or merged across scopes into the legacy view.

## Time semantics

Initial integration uses `TimeLegacyCoarseCoverage`: the pseudo-dataset covers
the tenant's time extent in its containing block, not exact per-series activity.
Dataset references and series min/max times cannot establish exact activity.

Direct tenant-wide attribute queries inherit the coarse behavior of the
corresponding TSDB route. Strict service-equality planning continues to select
real datasets so tenant-wide discovery does not broaden results across services
with different coverage. Partial-window equivalence requires explicit tests.

Exact activity would require real timestamps, an explicit interval convention,
and potentially activity bitmaps with boundary checks. Native exact activity,
independent time-based index compaction, and coverage manifests are future
extensions, not implied by selector-to-dataset parity.

## Validation and rollout gates

Before broad enablement, validate:

- Deterministic encoding, mapping corruption handling, unsupported features,
  short reads, and allocation/decompression limits.
- TSDB parity for equality, regex, negative/absent/empty-value matchers,
  conjunctions, discovery, and full/partial projection.
- Tenant isolation and rejection of out-of-range, cross-tenant, or pseudo-dataset
  references; full-metadata positions shifted by earlier tenants' indexes.
- Segment output against persisted labels; mixed-input and repeated-generation
  compaction with remapped output references and reused label memory.
- Default/opt-in routing, mixed requests, absence-only fallback, authoritative
  empty results, strict service selection, and partial time windows.
- Public metadata endpoints, actual backend execution, selective ranged reads,
  cancellation, memory exhaustion, and cleanup on failure.

Measure build and query latency, CPU, allocations, peak memory, output size,
ranged GET count, bytes fetched, and compression cost at realistic cardinalities
and compaction levels. Instrument bounded execution-path/build metrics without
attribute values or selectors as metric labels.

Deploy compatible readers and workers before depending on index availability.
Use explicit write enablement and limited dual-writing rollout, retain TSDB,
and keep request opt-in until parity and overhead are accepted. Rollback must
remain safe with mixed old/new blocks. Broad rollout also requires completed
compression, scale hardening, public-endpoint coverage, and representative
benchmarks; this document is a design contract, not a progress tracker.
