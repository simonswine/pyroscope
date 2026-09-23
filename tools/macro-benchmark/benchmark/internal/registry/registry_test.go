package registry

import (
	"slices"
	"testing"
)

func TestRegistration(t *testing.T) {
	original := benchmarks
	benchmarks = map[string]Benchmark{}
	t.Cleanup(func() { benchmarks = original })
	Register(Benchmark{Name: "z", Dataset: "first"})
	Register(Benchmark{Name: "a", Dataset: "first"})
	Register(Benchmark{Name: "other", Dataset: "second"})
	if got := Names("first"); !slices.Equal(got, []string{"a", "z"}) {
		t.Fatalf("unexpected names: %v", got)
	}
	names := Names("first")
	names[0] = "changed"
	if Names("first")[0] != "a" {
		t.Fatal("caller modified registry")
	}
	if len(Names("missing")) != 0 {
		t.Fatal("unknown dataset returned benchmarks")
	}
	for _, b := range []Benchmark{
		{Name: "a", Dataset: "second"},
		{Name: "", Dataset: "first"},
		{Name: "../escape", Dataset: "first"},
		{Name: "..", Dataset: "first"},
		{Name: "missing-dataset"},
	} {
		t.Run(b.Name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected invalid registration to panic")
				}
			}()
			Register(b)
		})
	}
}
