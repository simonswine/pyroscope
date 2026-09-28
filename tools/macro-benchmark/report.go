package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/pprof/profile"
	"github.com/grafana/pyroscope/macro-benchmark/internal/thirdparty/benchstat"
	"go.yaml.in/yaml/v3"
)

type reportProfile struct {
	Name               string      `json:"name"`
	Unit               string      `json:"unit"`
	Baseline           *reportTree `json:"baseline,omitempty"`
	Comparison         *reportTree `json:"comparison,omitempty"`
	BaselineSamples    int         `json:"baselineSamples"`
	ComparisonSamples  int         `json:"comparisonSamples"`
	BaselineDuration   int64       `json:"baselineDuration"`
	ComparisonDuration int64       `json:"comparisonDuration"`
	Warning            string      `json:"warning,omitempty"`
	File               string      `json:"file,omitempty"`
}
type reportTree struct {
	Name     string        `json:"name"`
	Self     int64         `json:"self"`
	Total    int64         `json:"total"`
	Children []*reportTree `json:"children,omitempty"`
}
type benchmarkReport struct {
	Name     string          `json:"name"`
	Stats    string          `json:"stats"`
	Metrics  []reportMetric  `json:"metrics"`
	Profiles []reportProfile `json:"profiles"`
}

type reportMetric struct {
	Key             string   `json:"key"`
	Scope           string   `json:"scope,omitempty"`
	Name            string   `json:"name"`
	Baseline        string   `json:"baseline"`
	Comparison      string   `json:"comparison"`
	Change          string   `json:"change"`
	Note            string   `json:"note"`
	BaselineValue   *float64 `json:"baselineValue,omitempty"`
	ComparisonValue *float64 `json:"comparisonValue,omitempty"`
	ChangeValue     *float64 `json:"changeValue,omitempty"`
}
type reportData struct {
	Benchmarks []benchmarkReport `json:"benchmarks"`
	Summary    reportSummary     `json:"summary"`
	Warning    string            `json:"warning,omitempty"`
}

type reportSummary struct {
	Baseline   targetRevision
	Comparison targetRevision
	Instance   string
	CPU        string
	Cores      int
	MemoryGiB  int
	Started    string
	Finished   string
	Duration   string
}

// readReportArchive accepts only profiles, benchmarks, and run metadata used
// by the report. It deliberately does not extract tar paths to the filesystem.
func readReportArchive(filename string) (map[string][]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	files := make(map[string][]byte)
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		name := strings.TrimPrefix(h.Name, "./")
		if path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("invalid archive path %q", name)
		}
		parts := strings.Split(name, "/")
		if name != "plan.yaml" && name != "cpu.txt" && name != "status.yaml" && name != "run.log" &&
			!(len(parts) == 3 && (parts[0] == "baseline" || parts[0] == "comparison") &&
				(parts[2] == "bench.txt" || parts[2] == "cpu.pprof" || parts[2] == "heap.pprof" || parts[2] == "ingest-cpu.pprof" || parts[2] == "ingest-heap.pprof")) {
			continue
		}
		if _, ok := files[name]; ok {
			return nil, fmt.Errorf("duplicate report input %q", name)
		}
		// Only the first log line is needed for older archives without
		// started_at. Do not retain potentially large worker logs in memory.
		if name == "run.log" {
			data, err := io.ReadAll(io.LimitReader(tr, 4096))
			if err != nil {
				return nil, err
			}
			files[name] = data
			continue
		}
		if h.Size > 64<<20 {
			return nil, fmt.Errorf("oversized report input %q", name)
		}
		data, err := io.ReadAll(io.LimitReader(tr, 64<<20+1))
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

func buildReport(files map[string][]byte) reportData {
	names := make(map[string]bool)
	for name := range files {
		p := strings.Split(name, "/")
		if len(p) == 3 {
			names[p[1]] = true
		}
	}
	var ordered []string
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	out := reportData{Summary: buildReportSummary(files)}
	for _, name := range ordered {
		b := benchmarkReport{Name: name}
		base := files["baseline/"+name+"/bench.txt"]
		comp := files["comparison/"+name+"/bench.txt"]
		if len(base) > 0 && len(comp) > 0 {
			var c benchstat.Collection
			c.AddConfig("baseline", base)
			c.AddConfig("comparison", comp)
			var buf bytes.Buffer
			tables := c.Tables()
			benchstat.FormatText(&buf, tables)
			b.Stats = buf.String()
			for _, table := range tables {
				for _, row := range table.Rows {
					if len(row.Metrics) != 2 {
						continue
					}
					metric := reportMetric{
						Key:             table.Metric,
						Scope:           row.Benchmark,
						Name:            row.Benchmark + " · " + table.Metric,
						Baseline:        formatBenchMean(row.Metrics[0], row.Scaler),
						Comparison:      formatBenchMean(row.Metrics[1], row.Scaler),
						Change:          row.Delta,
						Note:            row.Note,
						BaselineValue:   benchMeanValue(row.Metrics[0]),
						ComparisonValue: benchMeanValue(row.Metrics[1]),
					}
					if strings.HasSuffix(row.Delta, "%") && !math.IsNaN(row.PctDelta) && !math.IsInf(row.PctDelta, 0) {
						metric.ChangeValue = float64Ptr(row.PctDelta)
					}
					b.Metrics = append(b.Metrics, metric)
				}
			}
		} else {
			b.Stats = "Missing baseline or comparison bench.txt"
		}
		for _, p := range []string{"cpu", "heap", "ingest-cpu", "ingest-heap"} {
			rp := reportProfile{Name: p}
			if strings.Contains(p, "heap") {
				rp.Name = strings.Replace(p, "heap", "allocations", 1)
				rp.Unit = "bytes"
			} else {
				rp.Unit = "nanoseconds"
			}
			var a, z *profile.Profile
			for _, side := range []string{"baseline", "comparison"} {
				data := files[side+"/"+name+"/"+p+".pprof"]
				if len(data) == 0 {
					rp.Warning += side + " profile missing; "
					continue
				}
				prof, err := profile.ParseData(data)
				if err != nil {
					rp.Warning += side + " profile invalid: " + err.Error() + "; "
					continue
				}
				indexName := "cpu"
				if rp.Unit == "bytes" {
					indexName = "alloc_space"
				}
				idx, err := prof.SampleIndexByName(indexName)
				if err != nil {
					rp.Warning += err.Error() + "; "
					continue
				}
				tree := profileTree(prof, idx)
				if side == "baseline" {
					rp.Baseline = tree
					rp.BaselineSamples = len(prof.Sample)
					rp.BaselineDuration = prof.DurationNanos
					a = prof
				} else {
					rp.Comparison = tree
					rp.ComparisonSamples = len(prof.Sample)
					rp.ComparisonDuration = prof.DurationNanos
					z = prof
				}
			}
			if a != nil && z != nil {
				if rp.Unit == "nanoseconds" && (rp.BaselineDuration == 0 || rp.ComparisonDuration == 0 || float64(rp.ComparisonDuration)/float64(rp.BaselineDuration) > 1.2 || float64(rp.BaselineDuration)/float64(rp.ComparisonDuration) > 1.2) {
					rp.Warning += "different CPU capture durations; compare rates, not raw deltas; "
				}
				if rp.Unit == "nanoseconds" && (rp.Baseline.Total < 5e9 || rp.Comparison.Total < 5e9) {
					rp.Warning += "sparse CPU samples; "
				}
			}
			b.Profiles = append(b.Profiles, rp)
			b.Metrics = append(b.Metrics, profileMetric(rp))
		}
		out.Benchmarks = append(out.Benchmarks, b)
	}
	return out
}

func formatBenchMean(m *benchstat.Metrics, scaler benchstat.Scaler) string {
	if m == nil || m.Unit == "" {
		return "—"
	}
	return m.FormatMean(scaler)
}

func float64Ptr(v float64) *float64 { return &v }

func benchMeanValue(m *benchstat.Metrics) *float64 {
	if m == nil || m.Unit == "" || math.IsNaN(m.Mean) || math.IsInf(m.Mean, 0) {
		return nil
	}
	return float64Ptr(m.Mean)
}

func profileMetric(p reportProfile) reportMetric {
	metric := reportMetric{Key: "profile:" + p.Name, Note: "Sampled allocations over the process lifetime (including startup and warmup); descriptive only"}
	var a, b float64
	if p.Unit == "bytes" {
		metric.Name = p.Name + " · allocated bytes"
		if p.Baseline != nil {
			a = float64(p.Baseline.Total)
			metric.BaselineValue = float64Ptr(a)
			metric.Baseline = formatBinaryBytes(p.Baseline.Total)
		}
		if p.Comparison != nil {
			b = float64(p.Comparison.Total)
			metric.ComparisonValue = float64Ptr(b)
			metric.Comparison = formatBinaryBytes(p.Comparison.Total)
		}
	} else {
		metric.Name = p.Name + " · sampled CPU time"
		metric.Note = "Includes warmup and idle; capture durations can differ; descriptive only"
		if p.Baseline != nil {
			a = float64(p.Baseline.Total)
			metric.BaselineValue = float64Ptr(a)
			metric.Baseline = formatSampledTime(p.Baseline.Total)
		}
		if p.Comparison != nil {
			b = float64(p.Comparison.Total)
			metric.ComparisonValue = float64Ptr(b)
			metric.Comparison = formatSampledTime(p.Comparison.Total)
		}
		if p.BaselineDuration > 0 && p.ComparisonDuration > 0 {
			metric.Note += "; captures " + formatSampledTime(p.BaselineDuration) + " / " + formatSampledTime(p.ComparisonDuration)
		}
	}
	if metric.Baseline == "" {
		metric.Baseline = "—"
	}
	if metric.Comparison == "" {
		metric.Comparison = "—"
	}
	if metric.Baseline != "—" && metric.Comparison != "—" && a > 0 {
		metric.ChangeValue = float64Ptr((b/a - 1) * 100)
		metric.Change = fmt.Sprintf("%+.1f%%", *metric.ChangeValue)
	} else {
		metric.Change = "—"
	}
	if p.Warning != "" {
		metric.Note += "; " + strings.TrimSuffix(p.Warning, "; ")
	}
	return metric
}

func formatSampledTime(ns int64) string {
	switch {
	case ns >= 1e9:
		return fmt.Sprintf("%.2f s", float64(ns)/1e9)
	case ns >= 1e6:
		return fmt.Sprintf("%.2f ms", float64(ns)/1e6)
	case ns >= 1e3:
		return fmt.Sprintf("%.2f µs", float64(ns)/1e3)
	default:
		return fmt.Sprintf("%d ns", ns)
	}
}

func formatBinaryBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		value /= 1024
		if value < 1024 || unit == "PiB" {
			return fmt.Sprintf("%.2f %s", value, unit)
		}
	}
	return ""
}

func buildReportSummary(files map[string][]byte) reportSummary {
	var summary reportSummary
	var plan runPlan
	if err := yaml.Unmarshal(files["plan.yaml"], &plan); err == nil {
		summary.Baseline = plan.Baseline
		summary.Comparison = plan.Comparison
	}
	// Current provisioner uses this instance type (aws.go). Historical archives
	// record the CPU topology but not the EC2 instance type explicitly.
	if len(files["cpu.txt"]) > 0 {
		summary.Instance = "c6i.8xlarge"
		summary.MemoryGiB = 64
	}
	for _, line := range strings.Split(string(files["cpu.txt"]), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Model name":
			summary.CPU = strings.TrimSpace(value)
		case "CPU(s)":
			summary.Cores, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	var status struct {
		StartedAt  time.Time `yaml:"started_at"`
		FinishedAt time.Time `yaml:"finished_at"`
	}
	if err := yaml.Unmarshal(files["status.yaml"], &status); err == nil {
		if status.StartedAt.IsZero() {
			line, _, _ := strings.Cut(string(files["run.log"]), "\n")
			// The worker logs in UTC (main.go). Existing archives predate the
			// explicit started_at field; use their first recorded event.
			if len(line) >= 19 {
				status.StartedAt, _ = time.ParseInLocation("2006/01/02 15:04:05", line[:19], time.UTC)
			}
		}
		if !status.StartedAt.IsZero() {
			summary.Started = status.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		}
		if !status.FinishedAt.IsZero() {
			summary.Finished = status.FinishedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		}
		if !status.StartedAt.IsZero() && !status.FinishedAt.Before(status.StartedAt) {
			summary.Duration = formatRunDuration(status.FinishedAt.Sub(status.StartedAt))
		}
	}
	return summary
}

func formatRunDuration(d time.Duration) string {
	seconds := int64(d.Round(time.Second) / time.Second)
	hours, minutes := seconds/3600, seconds%3600/60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds%60)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func profileTree(p *profile.Profile, index int) *reportTree {
	root := &reportTree{Name: "all"}
	for _, s := range p.Sample {
		v := s.Value[index]
		if v <= 0 {
			continue
		}
		root.Total += v
		node := root
		for i := len(s.Location) - 1; i >= 0; i-- {
			loc := s.Location[i]
			for j := len(loc.Line) - 1; j >= 0; j-- {
				label := loc.Line[j].Function.Name
				if label == "" {
					label = "unknown"
				}
				var child *reportTree
				for _, c := range node.Children {
					if c.Name == label {
						child = c
						break
					}
				}
				if child == nil {
					child = &reportTree{Name: label}
					node.Children = append(node.Children, child)
				}
				child.Total += v
				node = child
			}
		}
		node.Self += v
	}
	sortTree(root)
	return root
}
func sortTree(n *reportTree) {
	sort.Slice(n.Children, func(i, j int) bool { return n.Children[i].Total > n.Children[j].Total })
	for _, c := range n.Children {
		sortTree(c)
	}
}

//go:embed report_assets/report.js
var reportJS string

//go:embed report_assets/report.css
var reportCSS string

var reportHTML = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Macro benchmark report</title>
<style>
body{font:var(--text-sm)/1.5 var(--font-sans);background:var(--bg-canvas);color:var(--text-primary);margin:0;padding:1rem 2vw}
h1{margin-top:0;color:var(--text-max-contrast)}
h2,h3{color:var(--text-primary)}
a{color:var(--text-link)}a:hover{color:var(--text-link-hover)}
a:focus-visible,summary:focus-visible,select:focus-visible,button:focus-visible{outline:2px solid var(--action-focus);outline-offset:2px}
.run-summary{display:flex;flex-wrap:wrap;gap:.8rem;margin:1rem 0 2rem}
.run-summary div{background:var(--bg-primary);border:1px solid var(--border-weak);border-radius:8px;padding:1rem;flex:1 1 240px;min-width:0}
.run-summary dt{color:var(--text-secondary);margin-bottom:.4rem}.run-summary dd{margin:0;overflow-wrap:anywhere}
section.benchmark{margin:.8rem 0;background:var(--bg-primary);border:1px solid var(--border-weak);border-radius:8px;padding:1rem;min-width:0}
section.benchmark h2{margin:0 0 .5rem}
details.flamegraph-details{border:1px solid var(--border-medium);border-radius:6px;min-width:0}
details.flamegraph-details>summary{cursor:pointer;padding:.8rem;font-size:1.1rem;font-weight:600}
details.flamegraph-details>summary:hover{background:var(--bg-elevated)}
.flamegraph-content{padding:0 .8rem;min-width:0}
.overview-controls{margin:1rem 0}.overview-controls select{margin-left:.5rem}
select{padding:.5rem;background:var(--bg-secondary);color:var(--text-primary);border:1px solid var(--border-medium);border-radius:6px;font:inherit}
.overview-scroll{overflow-x:auto}.overview{width:100%;border-collapse:collapse;background:var(--bg-primary)}
.overview th,.overview td{padding:.45rem .75rem;border-bottom:1px solid var(--border-weak);text-align:left;vertical-align:top}
.overview thead{background:var(--bg-secondary);color:var(--text-secondary)}
.overview .sort-heading{background:transparent;border:0;color:inherit;cursor:pointer;font:inherit;font-weight:600;text-align:left;padding:0}
.overview .sort-heading:hover,.overview th[aria-sort="ascending"] .sort-heading,.overview th[aria-sort="descending"] .sort-heading{color:var(--text-link)}
.overview tbody tr:hover{background:var(--action-hover)}
.overview td:nth-child(2),.overview td:nth-child(3),.overview td:nth-child(4){white-space:nowrap;font-variant-numeric:tabular-nums}
pre{overflow:auto;background:var(--bg-secondary);color:var(--text-primary);padding:1rem}.warning{color:var(--color-warning-text)}
.report-controls{display:flex;gap:.5rem;align-items:center;flex-wrap:wrap}
.report-flamegraph{height:max(600px,80vh);min-width:0;margin-bottom:1rem;background:var(--bg-primary);color:var(--text-primary)}
</style><style>{{.CSS}}</style>
<h1>Macro benchmark report</h1>
<dl class="run-summary">
<div><dt>Baseline</dt><dd>{{if .Summary.Baseline.Commit}}{{.Summary.Baseline.Ref}}<br><a href="https://github.com/grafana/pyroscope/commit/{{.Summary.Baseline.Commit}}">{{.Summary.Baseline.Commit}}</a>{{else}}Unavailable{{end}}</dd></div>
<div><dt>Comparison</dt><dd>{{if .Summary.Comparison.Commit}}{{.Summary.Comparison.Ref}}<br><a href="https://github.com/grafana/pyroscope/commit/{{.Summary.Comparison.Commit}}">{{.Summary.Comparison.Commit}}</a>{{else}}Unavailable{{end}}</dd></div>
<div><dt>Benchmark host</dt><dd>{{if .Summary.Instance}}{{.Summary.Instance}}{{else}}Instance type unavailable{{end}}{{if .Summary.CPU}}<br>{{.Summary.CPU}}{{end}}{{if .Summary.Cores}}<br>{{.Summary.Cores}} logical CPUs{{end}}{{if .Summary.MemoryGiB}} · {{.Summary.MemoryGiB}} GiB RAM{{end}}</dd></div>
<div><dt>Worker run (UTC)</dt><dd>Started: {{if .Summary.Started}}{{.Summary.Started}}{{else}}Unavailable{{end}}<br>Finished: {{if .Summary.Finished}}{{.Summary.Finished}}{{else}}Unavailable{{end}}<br>Elapsed: {{if .Summary.Duration}}{{.Summary.Duration}}{{else}}Unavailable{{end}}</dd></div>
</dl>
<p>Benchstat compares benchmark repetitions. Profile rows describe cluster processes, not the benchmark client. CPU values are total sampled CPU time (including warmup and idle; capture durations can differ); allocation values are sampled bytes allocated over the process lifetime (including startup and warmup), scaled in powers of 1024. Profile changes are not statistical tests. Serve this directory over HTTP to view flamegraphs (browsers restrict local file fetches).</p>
<h2>Overview</h2><div class="overview-viewer"></div>
{{range $i, $b := .Benchmarks}}<section class="benchmark" id="benchmark-{{$i}}"><h2>{{$b.Name}}</h2><pre>{{$b.Stats}}</pre><div class="benchmark-viewer"></div></section>{{end}}
<script type="application/json" id="report-data">{{.JSON}}</script><script>{{.Script}}</script></html>`))

func generateReport(archive, output string) error {
	files, err := readReportArchive(archive)
	if err != nil {
		return err
	}
	data := buildReport(files)
	if len(data.Benchmarks) == 0 {
		return errors.New("no benchmarks in archive")
	}
	// Keep the initial document small. Each profile is fetched only when opened.
	assetDir := strings.TrimSuffix(output, filepath.Ext(output)) + ".assets"
	if err := os.MkdirAll(assetDir, 0700); err != nil {
		return err
	}
	for bi := range data.Benchmarks {
		for pi := range data.Benchmarks[bi].Profiles {
			p := &data.Benchmarks[bi].Profiles[pi]
			filename := fmt.Sprintf("profile-%d-%d.json", bi, pi)
			payload, err := json.Marshal(struct {
				Baseline   *reportTree `json:"baseline,omitempty"`
				Comparison *reportTree `json:"comparison,omitempty"`
				Unit       string      `json:"unit"`
			}{p.Baseline, p.Comparison, p.Unit})
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(assetDir, filename), payload, 0600); err != nil {
				return err
			}
			p.Baseline, p.Comparison = nil, nil
			p.File = filepath.Base(assetDir) + "/" + filename
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	// JSON marshaling escapes HTML-significant characters. Script and CSS are
	// trusted, compile-time embedded assets, not archive content.
	view := struct {
		Benchmarks []benchmarkReport
		Summary    reportSummary
		JSON       template.JS
		Script     template.JS
		CSS        template.CSS
	}{data.Benchmarks, data.Summary, template.JS(raw), template.JS(reportJS), template.CSS(reportCSS)}
	if err := reportHTML.Execute(&buf, view); err != nil {
		return err
	}
	return os.WriteFile(output, buf.Bytes(), 0600)
}
