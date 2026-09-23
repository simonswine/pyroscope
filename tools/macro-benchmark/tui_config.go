package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark"
)

func configureRun(tty *os.File, store *stateStore, state *sessionState) error {
	fmt.Fprint(tty, "\x1b[H\x1b[2J\x1b[?25h")
	defer fmt.Fprint(tty, "\x1b[?25l")
	terminal := term.NewTerminal(tty, "")
	fmt.Fprintln(terminal, "Configure run (Enter keeps defaults; Ctrl-C cancels)")
	for _, b := range benchmark.All() {
		fmt.Fprintf(terminal, "  %-24s %s\n", b.Name, b.Dataset)
	}
	ask := func(label, current string) (string, error) {
		terminal.SetPrompt(label + " [" + terminalText(current, 160) + "]: ")
		value, err := terminal.ReadLine()
		if err != nil {
			return "", err
		}
		if value = strings.TrimSpace(value); value == "" {
			value = current
		}
		return value, nil
	}
	inputs := state.Config.Inputs
	selection := inputs.Benchmarks
	if selection == "" {
		selection = "all"
	}
	selection, err := ask("Benchmarks (comma-separated names or all)", selection)
	if err != nil {
		return err
	}
	inputs.Benchmarks, inputs.Dataset = selection, ""
	if selection == "all" {
		inputs.Benchmarks = ""
	}
	if inputs.BaselineRef, err = ask("Baseline Git ref", inputs.BaselineRef); err != nil {
		return err
	}
	if inputs.ComparisonRef, err = ask("Comparison Git ref", inputs.ComparisonRef); err != nil {
		return err
	}
	ingest := inputs.IngestRef
	if ingest == "" {
		ingest = "comparison"
	}
	if ingest, err = ask("Ingest Git ref (comparison follows comparison ref)", ingest); err != nil {
		return err
	}
	inputs.IngestRef = ingest
	if ingest == "comparison" {
		inputs.IngestRef = ""
	}
	count, err := ask("Repetitions per benchmark per version", strconv.Itoa(inputs.Count))
	if err != nil {
		return err
	}
	if inputs.Count, err = strconv.Atoi(count); err != nil {
		return err
	}
	if inputs.Count < 1 {
		return fmt.Errorf("repetitions must be positive")
	}
	if inputs.Benchtime, err = ask("Benchtime", inputs.Benchtime); err != nil {
		return err
	}
	inputs.defaults()
	// Validate before replacing the persisted draft; invalid answers change nothing.
	if err := inputs.validate(); err != nil {
		return err
	}
	selected, err := selectedBenchmarks(inputs)
	if err != nil {
		return err
	}
	fmt.Fprintf(terminal, "%d benchmarks, %d repetitions each, baseline %s, comparison %s\n", len(selected), inputs.Count, terminalText(inputs.BaselineRef, 160), terminalText(inputs.ComparisonRef, 160))
	state.Config.Inputs, state.Plan = inputs, nil
	return store.save(state)
}
