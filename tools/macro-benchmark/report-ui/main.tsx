// SPDX-License-Identifier: Apache-2.0
import { parsePprof, type Tree } from './pprof';
import { createRoot } from 'react-dom/client';
import { useEffect, useState } from 'react';
import { FlameGraph } from '@simonswine/grafana-flamegraph';
import type { ReportFrame } from './frame';
import '@simonswine/grafana-flamegraph/style.css';

type ProfileData = { baseline?: Tree; comparison?: Tree; unit: string };
type Profile = {
  name: string; unit: string; sampleType: string; warning?: string;
  baselineFile?: string; comparisonFile?: string;
  baselineSamples: number; comparisonSamples: number;
  baselineDuration: number; comparisonDuration: number;
};
type Metric = {
  key: string; scope?: string; name: string;
  baseline: string; comparison: string; change: string; note: string;
  baselineValue?: number; comparisonValue?: number; changeValue?: number;
};
type Benchmark = { name: string; benchmark?: string; profiles: Profile[]; metrics?: Metric[] };

// Preorder of the union tree is the nested-set format consumed by FlameGraph.
// Keep missing-side nodes so a new/removed stack remains visible in diff mode.
function toFrame({ baseline, comparison, unit }: ProfileData): ReportFrame | undefined {
  if (!baseline && !comparison) return undefined;
  const labels: string[] = [], levels: number[] = [], values: number[] = [], selfs: number[] = [];
  const valuesRight: number[] = [], selfsRight: number[] = [];
  function visit(left: Tree | undefined, right: Tree | undefined, level: number) {
    labels.push((left ?? right)!.name);
    levels.push(level);
    values.push(left?.total ?? 0);
    selfs.push(left?.self ?? 0);
    valuesRight.push(right?.total ?? 0);
    selfsRight.push(right?.self ?? 0);
    const children = new Map<string, [Tree | undefined, Tree | undefined]>();
    for (const child of left?.children ?? []) children.set(child.name, [child, undefined]);
    for (const child of right?.children ?? []) {
      const pair = children.get(child.name) ?? [undefined, undefined];
      pair[1] = child;
      children.set(child.name, pair);
    }
    for (const [l, r] of children.values()) visit(l, r, level + 1);
  }
  visit(baseline, comparison, 0);
  const numeric = (name: string, values: number[]) => ({ name, type: 'number' as const, values, config: { unit: unit === 'nanoseconds' ? 'ns' : unit } });
  const fields: ReportFrame['fields'] = [
    { name: 'label', type: 'string', values: labels, config: {} },
    numeric('level', levels), numeric('value', values), numeric('self', selfs),
  ];
  if (baseline && comparison) fields.push(numeric('valueRight', valuesRight), numeric('selfRight', selfsRight));
  else if (comparison) {
    fields[2] = numeric('value', valuesRight);
    fields[3] = numeric('self', selfsRight);
  }
  return { length: labels.length, fields };
}

function ProfileFlamegraph({ profile }: { profile: Profile }) {
  const [frame, setFrame] = useState<ReportFrame>();
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    const load = async (file: string | undefined) => {
      if (!file) return undefined;
      const response = await fetch(file, { signal: controller.signal });
      if (!response.ok) throw new Error(`HTTP ${response.status}: ${file}`);
      return parsePprof(new Uint8Array(await response.arrayBuffer()), profile.sampleType);
    };
    setFrame(undefined);
    setError('');
    Promise.all([load(profile.baselineFile), load(profile.comparisonFile)])
      .then(([baseline, comparison]) => {
        if (controller.signal.aborted) return;
        const next = toFrame({ baseline, comparison, unit: profile.unit });
        if (next) setFrame(next);
        else setError('No profiles were collected for this selection.');
      })
      .catch(err => { if (!controller.signal.aborted) setError(String(err)); });
    return () => controller.abort();
  }, [profile.baselineFile, profile.comparisonFile, profile.sampleType, profile.unit]);
  return <div className="report-flamegraph">
    {error ? <p role="alert">Failed to load profile: {error}</p> : frame ? <FlameGraph data={frame} /> : <p>Loading profile…</p>}
  </div>;
}

function Viewer({ benchmark }: { benchmark: Benchmark }) {
  const [selected, select] = useState(0);
  const [open, setOpen] = useState(false);
  const profile = benchmark.profiles[selected];
  return <details className="flamegraph-details" onToggle={e => setOpen(e.currentTarget.open)}>
    <summary>Flamegraph</summary>
    {open && <div className="flamegraph-content">
      <div className="report-controls">
        <label>Profile: <select aria-label={`${benchmark.name} profile`} value={selected} onChange={e => select(Number(e.target.value))}>
          {benchmark.profiles.map((p, i) => <option key={p.name} value={i}>{p.name}</option>)}
        </select></label>
        {profile.baselineFile && <a className="download-profile" href={profile.baselineFile} download={`baseline-${benchmark.name.replace(/[^a-zA-Z0-9._-]+/g, '-')}-${profile.name}.pprof`}>Download baseline pprof</a>}
        {profile.comparisonFile && <a className="download-profile" href={profile.comparisonFile} download={`comparison-${benchmark.name.replace(/[^a-zA-Z0-9._-]+/g, '-')}-${profile.name}.pprof`}>Download comparison pprof</a>}
      </div>
      <p className="warning">{profile.warning ?? ''} Baseline: {profile.baselineSamples} samples / {profile.baselineDuration} ns. Comparison: {profile.comparisonSamples} samples / {profile.comparisonDuration} ns.</p>
      <ProfileFlamegraph key={profile.name} profile={profile} />
    </div>}
  </details>;
}

function Overview({ benchmarks }: { benchmarks: Benchmark[] }) {
  const options = new Map<string, string>();
  for (const benchmark of benchmarks) {
    for (const metric of benchmark.metrics ?? []) {
      if (!options.has(metric.key)) {
        options.set(metric.key, metric.key.startsWith('profile:') ? metric.name : metric.key);
      }
    }
  }
  const [selected, setSelected] = useState(options.has('time/op') ? 'time/op' : (options.keys().next().value ?? ''));
  type SortKey = 'benchmark' | 'baseline' | 'comparison' | 'change';
  const [sort, setSort] = useState<{ key: SortKey; direction: 'asc' | 'desc' }>();
  const rows = benchmarks.flatMap((benchmark, i) => {
    const metrics = benchmark.metrics?.filter(m => m.key === selected) ?? [];
    // Profile captures span the entire benchmark; reported sub-benchmark
    // results, on the other hand, each deserve their own comparison row.
    return (metrics.length ? metrics : [undefined]).map(metric => {
      let label = benchmark.name;
      if (!benchmark.benchmark && metric?.scope && (metrics.length > 1 || metric.scope.includes('/'))) {
        const sub = metric.scope.includes('/') ? metric.scope.slice(metric.scope.indexOf('/') + 1) : metric.scope;
        label += ` / ${sub}`;
      }
      return { label, metric, benchmarkIndex: i };
    });
  }).map((row, order) => ({ ...row, order }));
  if (sort) {
    const value = (metric: Metric | undefined) => {
      switch (sort.key) {
        case 'baseline': return metric?.baselineValue;
        case 'comparison': return metric?.comparisonValue;
        case 'change': return metric?.changeValue;
        default: return undefined;
      }
    };
    rows.sort((a, b) => {
      let result: number;
      if (sort.key === 'benchmark') {
        result = a.label.localeCompare(b.label, undefined, { numeric: true });
        if (sort.direction === 'desc') result = -result;
      } else {
        const av = value(a.metric), bv = value(b.metric);
        // Missing data (including statistically insignificant benchstat deltas)
        // always belongs at the end in both directions.
        if (av == null || bv == null) result = av == null ? (bv == null ? 0 : 1) : -1;
        else result = sort.direction === 'asc' ? av - bv : bv - av;
      }
      return result || a.order - b.order;
    });
  }
  const heading = (key: SortKey, title: string) => <th scope="col" aria-sort={sort?.key === key ? (sort.direction === 'asc' ? 'ascending' : 'descending') : 'none'}>
    <button className="sort-heading" type="button" aria-label={`Sort by ${title}`} onClick={() => setSort(previous => ({ key, direction: previous?.key === key && previous.direction === 'asc' ? 'desc' : 'asc' }))}>
      {title} {sort?.key === key ? (sort.direction === 'asc' ? '↑' : '↓') : '↕'}
    </button>
  </th>;
  return <>
    <div className="overview-controls"><label>Metric
      <select aria-label="Overview metric" value={selected} onChange={e => setSelected(e.target.value)}>
        {[...options].map(([key, label]) => <option value={key} key={key}>{label}</option>)}
      </select>
    </label></div>
    <div className="overview-scroll"><table className="overview">
      <thead><tr>{heading('benchmark', 'Benchmark')}{heading('baseline', 'Baseline')}{heading('comparison', 'Comparison')}{heading('change', 'Change')}<th scope="col">Notes</th></tr></thead>
      <tbody>{rows.map(({ label, metric, benchmarkIndex, order }) => <tr key={order}>
        <th scope="row"><a href={`#benchmark-${benchmarkIndex}`}>{label}</a></th>
        <td>{metric?.baseline ?? '—'}</td><td>{metric?.comparison ?? '—'}</td>
        <td>{metric?.change ?? '—'}</td><td>{metric?.note ?? 'Not reported'}</td>
      </tr>)}</tbody>
    </table></div>
  </>;
}

const data = JSON.parse(document.getElementById('report-data')!.textContent!) as { benchmarks: Benchmark[] };
const overview = document.querySelector('.overview-viewer');
if (overview) createRoot(overview).render(<Overview benchmarks={data.benchmarks} />);
const viewers = document.querySelectorAll('.benchmark-viewer');
data.benchmarks.forEach((benchmark, i) => {
  if (viewers[i]) createRoot(viewers[i]).render(<Viewer benchmark={benchmark} />);
});
