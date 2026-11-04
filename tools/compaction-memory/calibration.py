#!/usr/bin/env python3
"""Fetch and summarize compaction-worker calibration logs (standard library only)."""

import argparse
import bisect
import csv
import datetime as dt
import json
import math
from pathlib import Path
import re
import shlex
import statistics
import subprocess


UNITS = {"B": 1, "KiB": 1024, "MiB": 1024**2, "GiB": 1024**3, "TiB": 1024**4}


def timestamp(value):
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise argparse.ArgumentTypeError("timestamps must include a timezone")
    return int(parsed.timestamp())


def iso(seconds):
    return dt.datetime.fromtimestamp(seconds, dt.timezone.utc).isoformat()


def fetch(args):
    if args.start >= args.end:
        raise ValueError("--start must precede --end")
    query = (
        "{namespace=" + json.dumps(args.namespace)
        + ",pod=~" + json.dumps(args.pods) + "}"
        ' |~ "compaction memory calibration|estimated compaction job memory"'
    )
    rows = {}

    def window(start, end):
        command = ["gcx"]
        if args.context:
            command += ["--context", args.context]
        command += [
            "logs", "query", "-d", args.datasource, query,
            "--from", iso(start), "--to", iso(end),
            "--limit", str(args.limit), "-o", "json",
        ]
        response = json.loads(subprocess.check_output(command, text=True))
        if response.get("status") != "success":
            raise RuntimeError(f"Loki query failed: {response}")
        if response.get("warnings"):
            raise RuntimeError(f"Loki returned warnings: {response['warnings']}")
        streams = response["data"]["result"]
        count = sum(len(stream["values"]) for stream in streams)
        if count >= args.limit:
            if end - start <= 1:
                raise RuntimeError("One second hit the limit; refusing to silently truncate")
            middle = (start + end) // 2
            window(start, middle)
            window(middle, end)
            return
        for stream in streams:
            pod = stream["stream"]["pod"]
            for entry in stream["values"]:
                # gcx emits objects; also accept Loki's native [timestamp, line].
                if isinstance(entry, dict):
                    stamp, line = entry["timestamp"], entry["line"]
                else:
                    stamp, line = entry[:2]
                key = (pod, str(stamp), line)
                rows[key] = {"pod": pod, "timestamp": str(stamp), "line": line}
        print(f"{iso(start)} .. {iso(end)}: {count} lines")

    for start in range(args.start, args.end, 3600):
        window(start, min(start + 3600, args.end))
    output = {
        "source": {"context": args.context, "datasource": args.datasource,
                   "query": query, "start": iso(args.start), "end": iso(args.end)},
        "logs": sorted(rows.values(), key=lambda row: int(row["timestamp"])),
    }
    args.output.write_text(json.dumps(output, indent=2) + "\n")
    print(f"Saved {len(rows)} lines to {args.output}")


def fields(line):
    return dict(token.split("=", 1) for token in shlex.split(line) if "=" in token)


def byte_value(value):
    number, unit = value.split()
    return float(number) * UNITS[unit]


def quantile(values, fraction):
    position = (len(values) - 1) * fraction
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    return values[lower] + (values[upper] - values[lower]) * (position - lower)


def median_interval(values):
    """Conservative exact 95% median CI via Binomial(n, 0.5) order statistics."""
    n = len(values)
    cumulative, rank = 0.0, 0
    for i in range(n + 1):
        cumulative += math.exp(
            math.lgamma(n + 1) - math.lgamma(i + 1)
            - math.lgamma(n - i + 1) - n * math.log(2)
        )
        if cumulative > 0.025:
            break
        rank = i + 1
    if rank == 0:
        return "unbounded"
    return f"{values[rank - 1]:.2f}–{values[n - rank]:.2f}"


def markdown_table(headers, rows):
    print("| " + " | ".join(headers) + " |")
    print("| " + " | ".join(["---"] + ["---:"] * (len(headers) - 1)) + " |")
    for row in rows:
        print("| " + " | ".join(map(str, row)) + " |")
    print()


def summarize(args):
    data = json.loads(args.input.read_text())
    logs = data["logs"] if isinstance(data, dict) else data
    estimates, completed = {}, []
    seen = set()
    for row in sorted(logs, key=lambda row: int(row["timestamp"])):
        identity = (row["pod"], row["timestamp"], row["line"])
        if identity in seen:
            continue
        seen.add(identity)
        f = fields(row["line"])
        job = f.get("job")
        if not job:
            continue
        key = (row["pod"], job)
        stamp = int(row["timestamp"])
        if f.get("msg") == "estimated compaction job memory":
            estimates.setdefault(key, []).append((stamp, f))
        elif f.get("msg") == "compaction memory calibration":
            completed.append((key, stamp, f))

    joined, unmatched, used = [], 0, set()
    for key, stamp, f in completed:
        candidates = estimates.get(key, [])
        index = bisect.bisect_right([item[0] for item in candidates], stamp) - 1
        # Match each attempt to its preceding estimate, never across pods.
        identity = (key, index)
        if index < 0 or identity in used:
            unmatched += 1
            continue
        used.add(identity)
        estimate = candidates[index][1]
        level = re.search(r"-L(\d+)$", key[1])
        row = {"pod": key[0], "job": key[1], "level": level[1] if level else "unknown",
               "timestamp_ns": stamp}
        for name in ("buffered_objects", "persistent_index", "dataset_working_set", "estimated_memory"):
            row[name + "_bytes"] = byte_value(estimate[name])
        for name in ("baseline_heap", "sampled_peak_heap", "peak_heap_increase", "allocated", "post_gc_heap"):
            row[name + "_bytes"] = byte_value(f[name])
        row["gc_cycles"] = int(f["gc_cycles"])
        row["retained_heap_delta_bytes"] = int(f["retained_heap_delta_bytes"])
        charge = row["estimated_memory_bytes"]
        row["peak_to_estimate"] = row["peak_heap_increase_bytes"] / charge if charge else None
        joined.append(row)

    print(f"Completed: {len(completed)}; matched: {len(joined)}; unmatched: {unmatched}")
    print(f"Unmatched estimates: {sum(map(len, estimates.values())) - len(used)}")
    if not joined:
        raise ValueError("No matched calibration jobs")
    with args.csv.open("w", newline="") as output:
        writer = csv.DictWriter(output, fieldnames=list(joined[0]))
        writer.writeheader()
        writer.writerows(joined)
    levels = sorted({row["level"] for row in joined},
                    key=lambda v: int(v) if v.isdigit() else math.inf)
    print("Mean heap across matched jobs (MiB; not time-averaged):")
    heap_rows = []
    for level in levels:
        jobs = [row for row in joined if row["level"] == level]
        means = [statistics.mean(row[name + "_bytes"] for row in jobs) / 1024**2
                 for name in ("baseline_heap", "sampled_peak_heap", "peak_heap_increase", "post_gc_heap")]
        heap_rows.append([f"L{level}", len(jobs), *(f"{value:.2f}" for value in means)])
    print()
    markdown_table(["Level", "Jobs", "Baseline", "Peak", "Peak increase", "Post-GC"], heap_rows)
    factor_rows = []
    for level in levels:
        values = sorted(row["peak_to_estimate"] for row in joined
                        if row["level"] == level and row["peak_to_estimate"] is not None)
        if not values:
            continue
        factor_rows.append([
            f"L{level}", len(values), f"{quantile(values, 0.5):.2f}x",
            median_interval(values),
            f"{quantile(values, 0.025):.2f}–{quantile(values, 0.975):.2f}",
            f"{values[0]:.2f}–{values[-1]:.2f}",
        ])
    markdown_table(["Level", "Jobs", "Median factor", "95% median CI", "Middle 95%", "Full range"],
                   factor_rows)
    print("CI assumes independent observations; logged sizes are rounded. Zero estimates excluded from factors.")
    print(f"Saved per-attempt data to {args.csv}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    download = commands.add_parser("fetch", help="Fetch all matching logs in a fixed time interval")
    download.add_argument("--context")
    download.add_argument("--datasource", required=True)
    download.add_argument("--namespace", required=True)
    download.add_argument("--pods", default=".*compaction-worker-calibration-.*")
    download.add_argument("--start", type=timestamp, required=True)
    download.add_argument("--end", type=timestamp, required=True)
    download.add_argument("--limit", type=int, default=5000, help="Per-request limit, not a total limit")
    download.add_argument("--output", type=Path, required=True)
    download.set_defaults(run=fetch)
    summary = commands.add_parser("summarize", help="Join estimates with measurements and export CSV")
    summary.add_argument("input", type=Path)
    summary.add_argument("--csv", type=Path, required=True)
    summary.set_defaults(run=summarize)
    args = parser.parse_args()
    if args.command == "fetch" and args.limit < 1:
        parser.error("--limit must be positive")
    args.run(args)


if __name__ == "__main__":
    main()
