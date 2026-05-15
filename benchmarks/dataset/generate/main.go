// Package main generates the benchmark dataset: synthetic pprof profile files
// and a manifest.json for use by the load generator and correctness checker.
//
// Usage (from repo root):
//
//	go run ./benchmarks/dataset/generate
//	go run ./benchmarks/dataset/generate -out /path/to/output
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/pprof/testhelper"
)

// fixedTS is a nanosecond timestamp used for all generated profiles so the
// output is deterministic across runs (2023-11-14 00:00:00 UTC).
const fixedTS = int64(1700000000) * 1_000_000_000

// Manifest describes the benchmark dataset directory layout.
// Must stay in sync with the type in benchmarks/load-generator/main.go.
type Manifest struct {
	Version  int            `json:"version"`
	Profiles []ProfileEntry `json:"profiles"`
}

// ProfileEntry is a single profile descriptor within manifest.json.
type ProfileEntry struct {
	File        string            `json:"file"`
	ServiceName string            `json:"service_name"`
	ProfileType string            `json:"profile_type"`
	SampleType  string            `json:"sample_type"`
	Labels      map[string]string `json:"labels"`
}

func main() {
	out := flag.String("out", "benchmarks/dataset", "output directory for profiles and manifest.json")
	flag.Parse()

	if err := os.MkdirAll(filepath.Join(*out, "profiles"), 0o755); err != nil {
		log.Fatalf("mkdir profiles: %v", err)
	}

	var entries []ProfileEntry

	type svcDef struct {
		name string
		cpu  func() []byte
		mem  func() []byte
	}

	services := []svcDef{
		{"service-a", serviceACPU, serviceAMemory},
		{"service-b", serviceBCPU, serviceBMemory},
		{"service-c", serviceCCPU, serviceCMemory},
	}

	sharedLabels := map[string]string{"namespace": "bench", "region": "us-east-1"}

	for _, svc := range services {
		for _, pt := range []struct {
			suffix      string
			profileType string
			sampleType  string
			gen         func() []byte
		}{
			{"cpu", "process_cpu", "cpu", svc.cpu},
			{"memory", "memory", "alloc_space", svc.mem},
		} {
			filename := svc.name + "-" + pt.suffix + ".pprof"
			relPath := "profiles/" + filename
			if err := os.WriteFile(filepath.Join(*out, "profiles", filename), pt.gen(), 0o644); err != nil {
				log.Fatalf("write %s: %v", relPath, err)
			}
			entries = append(entries, ProfileEntry{
				File:        relPath,
				ServiceName: svc.name,
				ProfileType: pt.profileType,
				SampleType:  pt.sampleType,
				Labels:      sharedLabels,
			})
			log.Printf("wrote %s", relPath)
		}
	}

	// Copy the real cpu.pprof from the repo's testdata directory as-is.
	// It is gzip-compressed protobuf (standard pprof format); Pyroscope handles
	// both compressed and uncompressed pprof on the ingest path.
	realSrc := filepath.Join("pkg", "og", "convert", "testdata", "cpu.pprof")
	if data, err := os.ReadFile(realSrc); err == nil {
		const realDest = "profiles/real-cpu.pprof"
		if err := os.WriteFile(filepath.Join(*out, "profiles", "real-cpu.pprof"), data, 0o644); err != nil {
			log.Fatalf("write real-cpu.pprof: %v", err)
		}
		entries = append(entries, ProfileEntry{
			File:        realDest,
			ServiceName: "real-service",
			ProfileType: "process_cpu",
			SampleType:  "cpu",
			Labels:      map[string]string{"namespace": "bench"},
		})
		log.Printf("copied %s → %s", realSrc, realDest)
	} else {
		log.Printf("warning: skipping real cpu.pprof (%v)", err)
	}

	writeManifest(filepath.Join(*out, "manifest.json"), entries)
	fmt.Printf("generated %d profiles in %s\n", len(entries), *out)
}

func writeManifest(path string, entries []ProfileEntry) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("create manifest: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Manifest{Version: 1, Profiles: entries}); err != nil {
		log.Fatalf("encode manifest: %v", err)
	}
	log.Printf("wrote manifest.json (%d entries)", len(entries))
}

// cpuBuilder returns a ProfileBuilder pre-configured for a CPU profile.
func cpuBuilder(service string) *testhelper.ProfileBuilder {
	return testhelper.NewProfileBuilderWithLabels(fixedTS, []*typesv1.LabelPair{
		{Name: "job", Value: service},
		{Name: "service_name", Value: service},
	}).CPUProfile()
}

// memBuilder returns a ProfileBuilder pre-configured for a memory profile.
func memBuilder(service string) *testhelper.ProfileBuilder {
	return testhelper.NewProfileBuilderWithLabels(fixedTS, []*typesv1.LabelPair{
		{Name: "job", Value: service},
		{Name: "service_name", Value: service},
	}).MemoryProfile()
}

// mustMarshal serialises a Profile to raw (uncompressed) protobuf bytes.
func mustMarshal(b *testhelper.ProfileBuilder) []byte {
	data, err := b.MarshalVT()
	if err != nil {
		log.Fatalf("marshal profile: %v", err)
	}
	return data
}

// ── service-a: HTTP web server ────────────────────────────────────────────────

func serviceACPU() []byte {
	b := cpuBuilder("service-a")
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "net/http.(*ServeMux).ServeHTTP", "handler.Index", "database/sql.(*DB).QueryContext").AddSamples(1_500_000)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "net/http.(*ServeMux).ServeHTTP", "handler.GetUser", "database/sql.(*DB).QueryContext", "database/sql.(*Stmt).QueryContext").AddSamples(900_000)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "net/http.(*ServeMux).ServeHTTP", "handler.CreatePost", "database/sql.(*DB).ExecContext").AddSamples(600_000)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "net/http.(*ServeMux).ServeHTTP", "handler.Auth", "crypto/bcrypt.CompareHashAndPassword").AddSamples(400_000)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "net/http.(*ServeMux).ServeHTTP", "handler.ListPosts", "encoding/json.Marshal").AddSamples(300_000)
	b.ForStacktraceString("main", "runtime.gcBgMarkWorker", "runtime.gcDrain").AddSamples(120_000)
	return mustMarshal(b)
}

func serviceAMemory() []byte {
	b := memBuilder("service-a")
	// values: alloc_objects, alloc_space(bytes), inuse_objects, inuse_space(bytes)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "handler.GetUser", "encoding/json.Marshal").AddSamples(5_000, 5_000_000, 1_200, 1_200_000)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "handler.ListPosts", "encoding/json.Marshal").AddSamples(8_000, 8_000_000, 2_000, 2_000_000)
	b.ForStacktraceString("main", "net/http.(*Server).Serve", "handler.Auth", "crypto/bcrypt.newFromHash").AddSamples(1_500, 1_500_000, 300, 300_000)
	b.ForStacktraceString("main", "runtime.gcBgMarkWorker", "runtime.newobject").AddSamples(2_000, 2_000_000, 400, 400_000)
	return mustMarshal(b)
}

// ── service-b: data processor ─────────────────────────────────────────────────

func serviceBCPU() []byte {
	b := cpuBuilder("service-b")
	b.ForStacktraceString("main", "processor.Run", "processor.parseBatch", "encoding/json.Unmarshal").AddSamples(2_000_000)
	b.ForStacktraceString("main", "processor.Run", "processor.transform", "strings.(*Builder).WriteString").AddSamples(1_200_000)
	b.ForStacktraceString("main", "processor.Run", "processor.encode", "compress/gzip.(*Writer).Write").AddSamples(800_000)
	b.ForStacktraceString("main", "processor.Run", "processor.flush", "net/http.(*Client).Do").AddSamples(500_000)
	b.ForStacktraceString("main", "processor.Run", "processor.parseBatch", "regexp.(*Regexp).FindAllString").AddSamples(350_000)
	b.ForStacktraceString("main", "runtime.gcBgMarkWorker", "runtime.gcDrain").AddSamples(150_000)
	return mustMarshal(b)
}

func serviceBMemory() []byte {
	b := memBuilder("service-b")
	b.ForStacktraceString("main", "processor.Run", "processor.parseBatch", "encoding/json.Unmarshal").AddSamples(12_000, 24_000_000, 3_000, 6_000_000)
	b.ForStacktraceString("main", "processor.Run", "processor.transform", "strings.(*Builder).grow").AddSamples(6_000, 12_000_000, 1_500, 3_000_000)
	b.ForStacktraceString("main", "processor.Run", "processor.encode", "bytes.(*Buffer).grow").AddSamples(4_000, 8_000_000, 1_000, 2_000_000)
	b.ForStacktraceString("main", "runtime.mallocgc", "runtime.newobject").AddSamples(3_000, 3_000_000, 600, 600_000)
	return mustMarshal(b)
}

// ── service-c: in-memory cache ────────────────────────────────────────────────

func serviceCCPU() []byte {
	b := cpuBuilder("service-c")
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "cache.(*LRU).Get", "sync.(*RWMutex).RLock").AddSamples(1_800_000)
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "cache.(*LRU).Set", "sync.(*RWMutex).Lock").AddSamples(900_000)
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "net.(*TCPConn).Read", "syscall.read").AddSamples(700_000)
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "net.(*TCPConn).Write", "syscall.write").AddSamples(500_000)
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "cache.(*LRU).evict", "runtime.mapdelete").AddSamples(400_000)
	b.ForStacktraceString("main", "runtime.gcBgMarkWorker", "runtime.gcDrain").AddSamples(100_000)
	return mustMarshal(b)
}

func serviceCMemory() []byte {
	b := memBuilder("service-c")
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "cache.(*LRU).Set", "runtime.newobject").AddSamples(7_000, 14_000_000, 4_000, 8_000_000)
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "cache.(*LRU).Get", "cache.marshal").AddSamples(3_000, 6_000_000, 1_000, 2_000_000)
	b.ForStacktraceString("main", "cache.(*Server).handleRequest", "net.(*TCPConn).Read", "bufio.(*Reader).fill").AddSamples(2_000, 4_000_000, 500, 1_000_000)
	b.ForStacktraceString("main", "runtime.mallocgc", "runtime.gcBgMarkWorker").AddSamples(1_000, 1_000_000, 200, 200_000)
	return mustMarshal(b)
}
