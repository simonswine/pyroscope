// Minimal frame shape accepted by the upstream nested-set flamegraph renderer.
// The report constructs these without depending on Grafana's DataFrame builder.
export type ReportFrame = {
  length: number;
  fields: Array<{
    name: string;
    type: 'string' | 'number';
    values: string[] | number[];
    config: { unit?: string };
  }>;
};
