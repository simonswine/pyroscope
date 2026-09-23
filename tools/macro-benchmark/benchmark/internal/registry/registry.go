// Package registry holds benchmark metadata registered during package initialization.
package registry

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type Benchmark struct {
	Name    string
	Dataset string
}

var benchmarks = map[string]Benchmark{}

// Register is called by benchmark packages from init, before concurrent use.
func Register(b Benchmark) {
	if b.Name == "" || b.Name == "." || b.Name == ".." || filepath.Base(b.Name) != b.Name || strings.ContainsAny(b.Name, `/\`) || b.Dataset == "" {
		panic(fmt.Sprintf("invalid benchmark registration: %+v", b))
	}
	if _, exists := benchmarks[b.Name]; exists {
		panic("duplicate benchmark registration: " + b.Name)
	}
	benchmarks[b.Name] = b
}

// All returns a sorted copy of registered benchmark metadata.
func All() []Benchmark {
	all := make([]Benchmark, 0, len(benchmarks))
	for _, b := range benchmarks {
		all = append(all, b)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

// Names returns a sorted copy of the names registered for a dataset.
func Names(dataset string) []string {
	var names []string
	for _, b := range benchmarks {
		if b.Dataset == dataset {
			names = append(names, b.Name)
		}
	}
	sort.Strings(names)
	return names
}
