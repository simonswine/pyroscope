# V2 profile-filter batching

The V2 `profileEntryIterator` uses bounded batches for required flat SeriesIndex,
TimeNanos, and optional StacktracePartition columns. It preserves the existing
page/chunk pruning, forward-seek policy, metrics, and async entry queue. Optional
profile IDs are projected for selected rows. Explicit profile-ID selectors retain
the scalar ID-first plan; unsupported schemas fall back before reading pages.
The `profile_filter.execution` trace attribute distinguishes batch/scalar plans.

No V1 selection integration, payload aggregation, parallelism, or storage-format
changes are included. The shared SyncIterator changes bound pooled decode capacity
and preserve read/close errors used by the batch scanner.

## Coverage

- Differential scans against the scalar join, including independent page/row-group
  boundaries, partial batches, empty selections, inclusive/negative time bounds,
  unsigned IDs, missing/unusable statistics, and schema rejection.
- Bounded buffer capacity, cancellation, read errors, and close errors.
- V2 entry equivalence: labels, fingerprints, partitions, IDs, grouping, sampled
  profiles, ID-first fallback, cancellation, and early close. Tests assert the
  batch implementation is actually selected. Existing Invoke fixtures also run.
- Synthetic CPU/allocation and ReaderAt request/byte benchmarks.
- Opt-in GCS cold/warm metadata, sparse/dense/narrow scans, four concurrent scans,
  and production V2 entry-factory equivalence with optional IDs.
- Opt-in downloaded-block local benchmark with correctness hashing outside timing.

```sh
go test ./pkg/phlaredb/... ./pkg/querybackend/... ./pkg/block
go test -race ./pkg/phlaredb/query ./pkg/querybackend ./pkg/block
go test ./pkg/phlaredb/query -run '^$' -bench '^BenchmarkProfileBatchScanner' -benchmem

PROFILE_BATCH_LOCAL_BLOCK=/absolute/path/to/block.bin \
  go test ./pkg/querybackend -run '^$' \
  -bench '^BenchmarkV2ProfileEntryLocal$' -benchmem -benchtime=1s -count=7

# Uses the active gcloud identity; read-only, incurs GCS requests/data transfer.
PROFILE_BATCH_GCS_URI=gs://bucket/path/block.bin \
  go test -tags=integration ./pkg/block ./pkg/querybackend \
  -run '^(TestProfileBatchRemote|TestV2ProfileEntryRemote)$' \
  -v -count=1 -timeout=15m -p=1
```

Remote tests and the downloaded-block benchmark skip unless explicitly enabled.
Both select the largest format-0 profile section, not every dataset in a block.
Cold/warm refers to Parquet metadata, not a flushed OS/GCS cache. HTTP counters
measure requests and consumed response-body bytes, not wire bytes. Local timing
uses warm OS/metadata caches and includes TSDB selection and entry production, but
not file opening or correctness hashing. Neither harness times full query payload
aggregation, symbolization, or a production endpoint.

## Validation on the clean branch

All listed unit/race tests and vet checks passed. Seven local benchmark samples
on the same 150,840-profile section gave median selection times of 97.6 ms scalar
versus 13.9 ms batch without IDs (7.0x), and 142.4 ms versus 55.5 ms with IDs (2.6x).
Remote ordered-result checks passed for every case, with identical request and
response-body byte counts. The production V2 entry test also passed with/without
IDs. Remote timing differences remained small; no broad remote speedup is claimed.
Raw validation output is local-only under `.tmp/`.
