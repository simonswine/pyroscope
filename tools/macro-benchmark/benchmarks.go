package main

import "github.com/grafana/pyroscope/macro-benchmark/benchmark"

func benchmarkNames(dataset string) []string {
	return benchmark.Names(dataset)
}
