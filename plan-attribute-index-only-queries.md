# Attribute-index support for index-only queries

## Implementation status

Implemented backend index selection, discovery-only section opening, legacy
metadata adapters, and bounded per-index-type trace counts. Added paired TSDB
parity tests, actual three-query block execution, mixed-request routing tests,
ranged-read coverage, and discovery tenant checks. The affected querybackend,
block, queryfrontend, and attributeindex packages pass with `go test -race`.
Multi-query coverage also exposed and fixed shared tracing-context mutation.

Remaining rollout work: public-endpoint integration coverage, broader failure
and partial-window fixtures, and representative performance benchmarks. The
header remains experimental; default routing is unchanged.

## Goal and scope

Extend the experimental opt-in in [the integration plan](plan-attribute-index-integration.md)
to answer index-only metadata queries directly from AttributeIndexV1:

- `QUERY_LABEL_NAMES`
- `QUERY_LABEL_VALUES`
- `QUERY_SERIES_LABELS` (including public endpoints implemented using this query)

Reuse `X-Pyroscope-Use-Attribute-Index: true` and the existing
`InvokeOptions.UseAttributeIndex` propagation. Preserve acceptance of `1` and
case-insensitive `true`. Do not add another header, public API, or wire field.
Absent/false opt-in keeps the existing TSDB behavior. This is experimental
read support, not a production-default cutover.

## Current code

- `pkg/frontend/readpath/queryfrontend/attribute_index.go` parses the header.
- `query_frontend.go` propagates the option and requests both pseudo-dataset
  types when planning tenant-wide index queries. An equality matcher on
  `service_name` instead selects explicit real datasets.
- `pkg/querybackend/query.go:datasetIndices` explicitly discards attribute
  pseudo-datasets for requests whose only dependency is `SectionTSDB`.
- The three query handlers above call TSDB directly. Their report aggregators
  can remain unchanged.
- `attributeindex.Reader.Names`, `Values`, and `Series` already provide the
  discovery/projection primitives. They are not drop-in replacements for all
  legacy output semantics.

## Routing contract

| Request / planned input | Execution |
| --- | --- |
| Header absent/false | Existing TSDB route |
| Opt-in, supported index-only queries, attribute index available | Execute directly on the tenant's attribute pseudo-dataset |
| Opt-in, supported index-only queries, attribute index absent | Execute directly on that tenant's TSDB pseudo-dataset |
| Explicit real datasets, including strict service selection | Existing per-dataset TSDB route, even with opt-in |
| Profile query or mixed metadata/profile request | Existing selector-to-dataset lookup, then existing real-dataset handlers |

Choose exactly one index per tenant **within each block**. Mixed blocks and
multi-tenant requests may therefore aggregate attribute and TSDB reports.
An attribute result containing zero matches is authoritative, not a reason to
retry TSDB. Corruption, I/O, resource-limit, and cancellation errors propagate;
do not hide them behind fallback. Initially fallback is absence-only, matching
the existing opt-in contract. Any future unsupported-format fallback needs
an explicitly classified capability error, not a catch-all retry.

Index-only execution must not call `DatasetIDs`, resolve real datasets, or
fetch full block metadata just to answer discovery queries.

## Work breakdown

### 1. Separate index selection from execution mode

Primary file: `pkg/querybackend/query.go`.

- Extract/reuse per-tenant index preference independently of whether execution
  performs dataset lookup or queries an index directly.
- Use an explicit supported-query allowlist for direct attribute execution;
  `SectionTSDB` alone is not a promise that every future handler supports it.
- For supported index-only requests, retain the selected pseudo-datasets in
  object metadata and execute handlers directly. Do not enter `lookupDatasets`.
- Keep profile and mixed-query requests on the existing lookup path.
- Make section opening format-aware: direct AttributeIndexV1 execution opens
  only `SectionAttributeIndex`; TSDB pseudo-datasets and real datasets keep
  their current dependencies. Keep request-level dependency classification
  separate from this physical-section mapping.
- Ensure no attribute pseudo-dataset can reach a TSDB-only handler, including
  unusual explicit-dataset/index mixtures. Reject invalid plans rather than
  accidentally opening attribute bytes as TSDB.
- Preserve dataset close/error cleanup, report aggregation, and accounting.
  Direct index execution must not count dataset mappings as processed profiles.

### 2. Add legacy metadata adapters

Primary files: `query_label_names.go`, `query_label_values.go`,
`query_series_labels.go`, and a small shared attribute adapter under
`pkg/querybackend/`.

Dispatch by the opened dataset format inside the existing handlers. Reuse
`attributeIndexMatchers` and return the same protobuf reports; do not emulate
the complete TSDB reader interface.

- Names: call `Names`, expose only `ScopeLegacy` names, sort and deduplicate.
- Values: call `Values` for a `ScopeLegacy` key and convert string values.
  Missing labels must not become synthetic empty values. Match TSDB's two
  paths: unfiltered discovery retains explicit empty values, while filtered
  discovery omits them (`LabelValueFor` treats empty values as absent).
- Series: call `Series`, applying all matchers before projection, then convert
  legacy string attributes to sorted label pairs and deduplicate after conversion.
- Validate the legacy string-value contract; never stringify typed attributes
  or merge same-named attributes from different scopes into legacy labels.
- Preserve internal persisted labels, including `__profile_type__`, just as
  the current TSDB handlers do. Never expose dataset references as labels.

Important compatibility cases from `getSeriesLabels`:

- No requested label names means full-series projection.
- Requested names with no intersection with available labels return no series,
  whereas the generic attribute reader can return an empty projected entity.
- TSDB series output omits empty-valued labels, even though unfiltered value
  discovery includes explicit empty values. Perform this normalization before deduplication.
- A matching series with no remaining labels can produce an empty label set
  when the requested projection intersects available names.

Keep these rules in the backend adapter rather than changing the generic typed
attribute-query API. Use complete equality for any new deduplication; do not
copy the existing fingerprint-only shortcut as the identity contract.

### 3. Preserve planning and time semantics

No frontend planning expansion is required for the first increment. Retain
strict service-equality selection of explicit datasets and its TSDB execution.
Using tenant-wide attribute discovery for that route could broaden results
when service datasets have different time ranges.

Direct attribute queries inherit tenant-wide coarse coverage, as does the
existing tenant-wide TSDB route. They do not prove per-series activity within
the request window. Test partial windows and disjoint service coverage against
the existing route; do not claim exact activity semantics.

Update header/option comments to describe both dataset lookup and metadata
queries. Verify the existing interceptor covers all public metadata endpoints
and that internal fan-out preserves the option. No protobuf change should be
needed; run generation if protobuf definitions/comments are changed.

### 4. Prove equivalence and fallback

Build paired TSDB/attribute indexes from identical persisted labels and compare
reports through the actual backend handlers, not only reader primitives.

Test matrix:

- All three query types, separately and in one index-only invocation.
- No matchers, equality, regex, negative matchers, absent labels, explicit empty
  values, impossible selectors, and conjunctions spanning different entities.
- Full/partial projection, repeated projection names, unknown names, empty
  projected sets, and entities collapsing to one normalized result.
- Old blocks, dual-index blocks, and mixed availability across tenants/blocks.
- Opt-in absent, false, true, and `1`; assert actual index selection, since
  matching output alone cannot prove the attribute path ran.
- Tenant isolation, strict service selection, partial-window boundaries, and
  profile/mixed requests retaining their existing execution mode.
- Corruption, cancellation, memory-budget exhaustion, and cleanup; an empty
  attribute result must not cause TSDB fallback.
- Object-store read assertions: no profile/symbol sections, no dataset lookup,
  and no automatic whole-block fetch for direct attribute queries.

Extend `pkg/querybackend/query_attribute_index_test.go`, frontend header/planning
coverage, and `pkg/test/integration/microservices_test.go` for end-to-end public
metadata requests. Include higher-level endpoints backed by series-label queries.

### 5. Observe and benchmark

- Preserve the existing index-only trace marker and add bounded execution-path
  attributes distinguishing attribute, TSDB, and absence fallback. Do not label
  metrics with attribute names, values, or selectors.
- Benchmark names, values, and series projection against tenant-wide TSDB at
  representative cardinalities, with and without selective matchers.
- Measure latency, allocations, peak memory, ranged GET count, bytes read, and
  decompression work. The current unfiltered `Values` implementation loads
  dictionaries broadly; measure rather than assume optimal key-selective reads.
- Keep request opt-in and TSDB fallback throughout rollout. No write-path or
  index-format changes are part of this increment.

## Suggested PR sequence

1. Routing/section selection and legacy adapters, with paired-index unit tests.
2. Frontend/integration parity, failure-path and ranged-read coverage, plus
   updated opt-in comments and cross-linked plans.
3. Benchmarks and bounded execution-path instrumentation; optimize only from
   measured results.

## Acceptance criteria

- The existing header enables direct attribute execution for all three supported
  index-only query types when tenant-wide indexes are planned.
- Reports match the current TSDB route, including projection/empty-value behavior.
- Missing indexes fall back per tenant/block; failed or empty queries do not.
- Default, explicit-service, profile, and mixed-query behavior remains unchanged.
- Direct execution reads only index data and preserves cancellation, bounded
  reader resources, tenant isolation, and existing aggregation contracts.
