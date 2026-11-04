# Compaction memory calibration

Fetch calibration logs through `gcx`, join them to admission estimates, and summarize
peak-to-estimate factors by compaction level. Uses Python 3.9+ (standard library
only) and an authenticated `gcx` context with access to the Loki datasource.
Nothing here modifies the cluster or its configuration.

## Enable calibration

Deploy dedicated compaction workers with:

```text
-compaction-worker.calibration=true
```

This overrides job concurrency to one, forces GC before and after each compaction,
and samples process heap every 25 ms during compaction. It logs estimates even
when byte budgeting is disabled (the default). Calibration affects throughput,
GC, and pool retention; do not enable it fleet-wide as normal operating behavior.
Other goroutines in the process can still allocate during a job.

### Compare with more aggressive GC

For a separate diagnostic run, set the worker container's environment variable:

```yaml
env:
  - name: GOGC
    value: "20"
```

Keep `-compaction-worker.calibration=true` enabled. `GOGC=20` lowers the GC heap
growth target and generally triggers more frequent collection; it does not force
GC on a fixed timer. This can reduce uncollected garbage in the sampled heap,
but increases GC CPU overhead and can affect job duration and pool retention.
It cannot reclaim memory still referenced by an active job.

Compare against a run using production GC settings, keeping input jobs,
concurrency, and `GOMEMLIMIT` unchanged. Compare peak heap, job duration, GC CPU
and cycles, and container memory. Keep captures and summaries separate and label
them with the GC settings: factors measured with `GOGC=20` may underestimate
memory needed by workers using production GC settings. Use the production-settings
run for admission calibration; use the aggressive-GC run to investigate how much
of the peak is collectible garbage, not as a measurement of the exact live heap.

## Find the rollout time

For example, with the `ops-ops` context and Grafana Logging Dev datasource:

```sh
gcx --context ops-ops logs query -d OP27Xzxnk \
  '{namespace="fire-dev-001",pod=~"fire-compaction-worker-calibration-.*"} |= "compaction memory calibration enabled"' \
  --since 24h --limit 100 -o raw
```

Use an explicit start just before the rollout and a fixed end for reproducibility.
The following dates are an example historical capture; adjust them for your run.

## Fetch all jobs

```sh
python3 tools/compaction-memory/calibration.py fetch \
  --context ops-ops \
  --datasource OP27Xzxnk \
  --namespace fire-dev-001 \
  --pods 'fire-compaction-worker-calibration-.*' \
  --start 2026-11-03T17:35:00Z \
  --end 2026-11-04T08:52:00Z \
  --output /tmp/calibration.json
```

There is **no total result limit**. The tool queries hourly windows, recursively
splits windows that reach the per-request limit (default 5,000), and deduplicates
inclusive window boundaries. It fails rather than silently truncating if even a
one-second window reaches the limit. Set `--limit` to a positive value supported
by your Loki deployment. Unlike `gcx --limit 0`, this does not rely on Loki's
default result limit. It retrieves logs still available in Loki, not logs lost
before ingestion or removed by retention.

The JSON includes the query, context, datasource, interval, and raw log records.
Keep captures outside the repository: they can contain internal job/tenant IDs.
Large histories are held in memory; split very long captures into fixed intervals.

## Summarize and export

```sh
python3 tools/compaction-memory/calibration.py summarize \
  /tmp/calibration.json --csv /tmp/calibration-jobs.csv
```

Estimates and completion measurements are joined by **pod + job**, using the latest
preceding estimate for each attempt. An estimate is not reused for multiple
completions. Unmatched estimates/completions are reported: jobs crossing capture
boundaries, crashes, or missing logs can cause these. The summary includes only
matched attempts; do not treat it as a complete view of failed/OOM jobs.

The CSV includes the three estimate components, total estimate, baseline and peak
heap, peak increase, cumulative allocations, post-GC heap, retained delta, GC
cycles, and peak-to-estimate ratio. The summary also reports per-level arithmetic
mean baseline heap, sampled peak heap, peak heap increase, and post-GC heap in
MiB across all matched attempts, including those with zero estimates. These are
averages across jobs, not time-averaged heap usage during a job (which the logs
do not provide).

Both summary tables are printed as Markdown, ready to paste into an issue or
report; no `column` filter is needed.

The factor summary reports per-level counts, median
ratio, a conservative exact 95% median confidence interval from binomial order
statistics, empirical 2.5th/97.5th percentiles, and the observed full range.

## Interpretation and caveats

```text
peak_heap_increase = sampled_peak_heap - baseline_heap
factor = peak_heap_increase / estimated_memory
```

- Baseline is measured after forced GC immediately before compaction. It is
  already subtracted from `peak_heap_increase`; do not subtract it again.
- `sampled_peak_heap` is a sampled process-wide high-water mark, not an exact
  allocation peak. Spikes between samples can be missed.
- `allocated` is the increase in `TotalAlloc`: allocation churn, not simultaneous
  retained memory. RSS is not used in these calculations.
- Logged human-readable sizes are rounded. Ratios and reconstructed byte counts
  are approximate. Zero estimates are excluded from ratio statistics.
- `gc_cycles` excludes the forced baseline and final GCs. The signed retained
  delta compares post-GC heap after the job to the pre-job baseline.
- Pools still matter: baseline pool memory is subtracted, pool growth contributes
  to the peak, and forced GC can discard `sync.Pool` contents. This is not a
  pool-independent cost. Compare cold/warm runs and runs without forced pre-job
  GC before extrapolating to normal production behavior.
- The CI is for the **median**, not a prediction interval for individual jobs.
  It assumes independent samples; repeated similar workloads on a few pods are
  correlated. A narrow CI does not establish generalization to other workloads.
- Compare multiple levels and sizes. Do not turn one dataset's median ratio into
  a universal admission multiplier or an OOM guarantee.

## Tests

```sh
python3 -m unittest discover -s tools/compaction-memory -p 'test_*.py'
```
