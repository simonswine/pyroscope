import { useState } from "react";

import { createTheme } from "@grafana/data";
import FlameGraphContainer from "./upstream/dist/esm/FlameGraphContainer.mjs";
import type { ReportFrame } from "./frame";

import "./ComparisonFlameGraph.css";

type View = "baseline" | "comparison" | "diff";

interface Props {
  baseline?: ReportFrame;
  comparison?: ReportFrame;
  diff?: ReportFrame;
  label: string;
}

// The view mode belongs to the flamegraph, not the benchmark's profile picker.
export default function ComparisonFlameGraph({
  baseline,
  comparison,
  diff,
  label,
}: Props) {
  const [view, setView] = useState<View>("baseline");
  const data = { baseline, comparison, diff }[view];
  const theme = createTheme({ colors: { mode: "dark" } });

  return (
    <div className="report-comparison-flamegraph">
      <div
        className="report-comparison-toolbar"
        role="group"
        aria-label={`${label} flamegraph view`}
      >
        {(["baseline", "comparison", "diff"] as const).map((option) => (
          <button
            key={option}
            type="button"
            aria-pressed={view === option}
            onClick={() => setView(option)}
          >
            {option === "diff"
              ? "Diff"
              : option === "baseline"
                ? "Baseline"
                : "Comparison"}
          </button>
        ))}
      </div>
      <div className="report-comparison-body">
        {data ? (
          <FlameGraphContainer
            key={view}
            data={data as never}
            getTheme={() => theme}
            enableNewUI
            showAnalyzeWithAssistant={false}
          />
        ) : (
          <p>Profile unavailable</p>
        )}
      </div>
    </div>
  );
}
