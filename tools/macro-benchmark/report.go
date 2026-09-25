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
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/google/pprof/profile"
	"github.com/grafana/pyroscope/macro-benchmark/internal/thirdparty/benchstat"
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
	Profiles []reportProfile `json:"profiles"`
}
type reportData struct {
	Benchmarks []benchmarkReport `json:"benchmarks"`
	Warning    string            `json:"warning,omitempty"`
}

// readReportArchive accepts only the small set of files used by the report. It
// deliberately does not extract tar paths to the filesystem.
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
		if len(parts) != 3 || (parts[0] != "baseline" && parts[0] != "comparison") || (parts[2] != "bench.txt" && parts[2] != "cpu.pprof" && parts[2] != "heap.pprof" && parts[2] != "ingest-cpu.pprof" && parts[2] != "ingest-heap.pprof") {
			continue
		}
		if h.Size > 64<<20 {
			return nil, fmt.Errorf("oversized report input %q", name)
		}
		if _, ok := files[name]; ok {
			return nil, fmt.Errorf("duplicate report input %q", name)
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
	out := reportData{}
	for _, name := range ordered {
		b := benchmarkReport{Name: name}
		base := files["baseline/"+name+"/bench.txt"]
		comp := files["comparison/"+name+"/bench.txt"]
		if len(base) > 0 && len(comp) > 0 {
			var c benchstat.Collection
			c.AddConfig("baseline", base)
			c.AddConfig("comparison", comp)
			var buf bytes.Buffer
			benchstat.FormatText(&buf, c.Tables())
			b.Stats = buf.String()
		} else {
			b.Stats = "Missing baseline or comparison bench.txt"
		}
		for _, p := range []string{"cpu", "heap", "ingest-cpu", "ingest-heap"} {
			rp := reportProfile{Name: p}
			if strings.Contains(p, "heap") {
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
					indexName = "inuse_space"
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
		}
		out.Benchmarks = append(out.Benchmarks, b)
	}
	return out
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

var reportFontImport = regexp.MustCompile(`^@import "https://fonts\.googleapis\.com/[^\"]+";`)

var reportHTML = template.Must(template.New("report").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Macro benchmark report</title><style>body{font:14px system-ui;background:#161b29;color:#eee;max-width:1200px;margin:2rem auto;padding:0 1rem}section{padding:1rem;margin:1rem 0;background:#252c3e;border-radius:8px}pre{overflow:auto;background:#111827;padding:1rem} .warning{color:#ffc27d} select{padding:.5rem} details{margin:.8rem 0}.report-flamegraph{height:480px;min-width:0;margin-bottom:2rem;background:var(--bg-primary);color:var(--text-primary)}</style><style>{{.CSS}}</style><h1>Macro benchmark report</h1><p>Benchstat compares benchmark repetitions. Profiles cover the cluster processes, not the benchmark client. CPU captures include warmup and idle; heap profiles are post-GC snapshots.</p>{{range .Benchmarks}}<section><h2>{{.Name}}</h2><pre>{{.Stats}}</pre><div class="benchmark-viewer"></div></section>{{end}}<script type="application/json" id="report-data">{{.JSON}}</script><script>{{.Script}}</script></html>`))

func generateReport(archive, output string) error {
	files, err := readReportArchive(archive)
	if err != nil {
		return err
	}
	data := buildReport(files)
	if len(data.Benchmarks) == 0 {
		return errors.New("no benchmarks in archive")
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
		JSON       template.JS
		Script     template.JS
		CSS        template.CSS
	}{data.Benchmarks, template.JS(raw), template.JS(reportJS), template.CSS(reportFontImport.ReplaceAllString(reportCSS, ""))}
	if err := reportHTML.Execute(&buf, view); err != nil {
		return err
	}
	return os.WriteFile(output, buf.Bytes(), 0600)
}
