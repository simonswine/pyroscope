package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/charmbracelet/x/ansi"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark"
)

// configureRun shows a full-screen form. Arrow keys / Tab move between fields;
// checkboxes are toggled with Space, 'a' toggles all; Enter on the Save row
// commits. Esc or Ctrl-C cancels without saving.
func configureRun(tty *os.File, store *stateStore, state *sessionState) error {
	// Resolve a ref in the background; return short commit or error string.
	resolveRef := func(ref string) string {
		if ref == "" {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		source := state.SourceDir
		cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		cmd.Dir = source
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "unknown ref"
		}
		commit := strings.TrimSpace(string(out))
		if len(commit) > 12 {
			commit = commit[:12]
		}
		return commit
	}

	allBenchmarks := benchmark.All()

	// --- form state ---
	type refField struct {
		label string
		value string
		hint  string // resolved commit or error, filled on blur
	}

	inputs := state.Config.Inputs
	inputs.defaults()

	// Which benchmarks are checked.
	checked := make([]bool, len(allBenchmarks))
	if inputs.Benchmarks == "" {
		// all selected
		for i := range checked {
			checked[i] = true
		}
	} else {
		names := map[string]bool{}
		for _, n := range strings.Split(inputs.Benchmarks, ",") {
			names[strings.TrimSpace(n)] = true
		}
		for i, b := range allBenchmarks {
			checked[i] = names[b.Name]
		}
	}

	baseline := inputs.BaselineRef
	comparison := inputs.ComparisonRef
	ingest := inputs.IngestRef
	if ingest == "" {
		ingest = "comparison"
	}
	countStr := strconv.Itoa(inputs.Count)
	benchtime := inputs.Benchtime

	baselineHint := resolveRef(baseline)
	comparisonHint := resolveRef(comparison)
	ingestHint := ""
	if ingest != "comparison" {
		ingestHint = resolveRef(ingest)
	} else {
		ingestHint = "follows comparison"
	}

	// Fields: 0=baseline, 1=comparison, 2=ingest, 3=count, 4=benchtime,
	//         5..5+N-1 = checkboxes, last = Save
	const (
		fBaseline   = 0
		fComparison = 1
		fIngest     = 2
		fCount      = 3
		fBenchtime  = 4
		fCheckboxes = 5
	)
	fSave := fCheckboxes + len(allBenchmarks)
	totalFields := fSave + 1

	cursor := 0 // which field is active

	width, height := 100, 30
	if w, h, err := term.GetSize(int(tty.Fd())); err == nil && w > 0 {
		width, height = w, h
	}

	// Text editing state for text fields.
	textFields := map[int]*string{
		fBaseline:   &baseline,
		fComparison: &comparison,
		fIngest:     &ingest,
		fCount:      &countStr,
		fBenchtime:  &benchtime,
	}

	isTextField := func(f int) bool {
		_, ok := textFields[f]
		return ok
	}
	isCheckbox := func(f int) bool {
		return f >= fCheckboxes && f < fSave
	}

	// cursor positions within text fields
	cursors := map[int]int{}
	for f, s := range textFields {
		cursors[f] = len(*s)
	}

	// When leaving a ref field, resolve it.
	onBlur := func(f int) {
		switch f {
		case fBaseline:
			baselineHint = resolveRef(baseline)
		case fComparison:
			comparisonHint = resolveRef(comparison)
		case fIngest:
			if ingest == "comparison" {
				ingestHint = "follows comparison"
			} else {
				ingestHint = resolveRef(ingest)
			}
		}
	}

	move := func(delta int) {
		onBlur(cursor)
		cursor = (cursor + delta + totalFields) % totalFields
		// Sync cursor-within-text to end of new field
		if isTextField(cursor) {
			cursors[cursor] = len(*textFields[cursor])
		}
	}

	errorMsg := ""

	scroll := 0
	render := func() {
		width, height = terminalSize(tty)

		allChecked := true
		noneChecked := true
		for _, c := range checked {
			if c {
				noneChecked = false
			} else {
				allChecked = false
			}
		}

		var rows []string
		focusRow := 0

		const (
			reset = "\x1b[0m"
			dim   = "\x1b[2m"
			cyan  = "\x1b[1;36m"
			amber = "\x1b[33m"
			rev   = "\x1b[7m"
			green = "\x1b[32m"
			red   = "\x1b[31m"
			bold  = "\x1b[1m"
		)

		line := func(s string) { rows = append(rows, s) }

		line(cyan + " CONFIGURE SESSION" + reset + dim + "   ↑↓/Tab move · Space toggle · a toggle-all · Enter confirm · Esc cancel" + reset)
		line(dim + strings.Repeat("─", max(1, width-1)) + reset)

		renderField := func(idx int, label, value, hint string) {
			active := cursor == idx
			if active {
				focusRow = len(rows) - 2
			}
			prefix := "  "
			labelStyle := ""
			valStyle := dim
			if active {
				prefix = cyan + "› " + reset
				labelStyle = bold
				valStyle = ""
			}
			hintStr := ""
			if hint != "" {
				hintCol := green
				if hint == "unknown ref" {
					hintCol = red
				} else if hint == "follows comparison" {
					hintCol = dim
				}
				hintStr = "  " + hintCol + hint + reset
			}
			// Show cursor inside active text field.
			displayVal := value
			if active {
				cp := cursors[idx]
				if cp < 0 {
					cp = 0
				}
				if cp > len([]rune(value)) {
					cp = len([]rune(value))
				}
				r := []rune(value)
				var cursorChar string
				if cp < len(r) {
					cursorChar = rev + string(r[cp]) + reset
					displayVal = string(r[:cp]) + cursorChar + string(r[cp+1:])
				} else {
					displayVal = value + rev + " " + reset
				}
			}
			labelWidth := min(16, max(4, width/3))
			valueWidth := max(1, width-labelWidth-5)
			if active {
				cp := min(cursors[idx], len([]rune(value)))
				column := ansi.StringWidth(string([]rune(value)[:cp]))
				start := max(0, column-valueWidth+1)
				displayVal = ansi.Cut(displayVal, start, start+valueWidth)
			} else {
				displayVal = ansi.Truncate(displayVal, valueWidth, "")
			}
			paddedLabel := padVisual(ansi.Truncate(label, labelWidth, ""), labelWidth)
			paddedVal := padVisual(valStyle+displayVal, min(40, valueWidth))
			row := fmt.Sprintf("%s%s%s%s %s%s%s",
				prefix, labelStyle, paddedLabel, reset,
				paddedVal, reset,
				hintStr)
			line(row)
		}

		renderField(fBaseline, "Baseline ref", baseline, baselineHint)
		renderField(fComparison, "Comparison ref", comparison, comparisonHint)
		renderField(fIngest, "Ingest ref", ingest, ingestHint)
		renderField(fCount, "Repetitions", countStr, "")
		renderField(fBenchtime, "Benchtime", benchtime, "")

		line("")
		toggleHint := ""
		if allChecked {
			toggleHint = dim + "  (all selected)" + reset
		} else if noneChecked {
			toggleHint = dim + "  (none selected)" + reset
		} else {
			n := 0
			for _, c := range checked {
				if c {
					n++
				}
			}
			toggleHint = dim + fmt.Sprintf("  (%d/%d)", n, len(allBenchmarks)) + reset
		}
		line(dim + "  BENCHMARKS  " + reset + dim + "Space toggles · a toggles all" + reset + toggleHint)

		for i, bm := range allBenchmarks {
			idx := fCheckboxes + i
			active := cursor == idx
			if active {
				focusRow = len(rows) - 2
			}
			prefix := "  "
			if active {
				prefix = cyan + "› " + reset
			}
			box := "[ ]"
			boxStyle := dim
			if checked[i] {
				box = "[✓]"
				boxStyle = green
			}
			if active {
				boxStyle = bold
			}
			line(fmt.Sprintf("%s%s%s%s %-28s%s%s",
				prefix, boxStyle, box, reset,
				bm.Name,
				dim, bm.Dataset+reset))
		}

		bodyEnd := len(rows)
		saveStyle := dim
		if cursor == fSave {
			saveStyle = rev
		}
		line("  " + saveStyle + "  Save  " + reset + dim + "  (or press Enter on any field to advance)" + reset)

		line(dim + "  Esc cancel · ↑↓/Tab move · Enter advances / saves" + reset)
		status := ""
		if errorMsg != "" {
			status = red + "  Error: " + terminalText(errorMsg, 0) + reset
		}
		line(status)
		view, offset := renderConfigScreen(width, height, rows[:2], rows[2:bodyEnd], rows[bodyEnd:], focusRow, scroll, cursor != fSave)
		scroll = offset
		fmt.Fprint(tty, view)
	}

	// Switch terminal to raw mode so we get individual keystrokes.
	previous, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return err
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			term.Restore(int(tty.Fd()), previous) //nolint:errcheck
		}
	}
	defer restore()

	// The active text field draws its own cursor; hide the hardware cursor.
	fmt.Fprint(tty, "\x1b[?25l")
	defer fmt.Fprint(tty, "\x1b[?25l")

	var escBuf [8]byte

retry:
	render()
	for {
		// Periodically redraw so terminal resizes do not require a keystroke.
		fd := int(tty.Fd())
		if fd >= unix.FD_SETSIZE {
			return errors.New("terminal file descriptor exceeds select capacity")
		}
		var readable unix.FdSet
		readable.Set(fd)
		timeout := unix.NsecToTimeval(int64(250 * time.Millisecond))
		ready, err := unix.Select(fd+1, &readable, nil, nil, &timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if ready == 0 {
			render()
			continue
		}
		n, err := tty.Read(escBuf[:])
		if err != nil || n == 0 {
			return err
		}
		keys := escBuf[:n]

		// Ctrl-C / Ctrl-D / Esc
		if keys[0] == 3 || keys[0] == 4 || keys[0] == 27 && n == 1 {
			fmt.Fprint(tty, "\x1b[H\x1b[2J")
			return fmt.Errorf("cancelled")
		}

		// Arrow keys and special sequences (ESC [ A/B/C/D)
		if keys[0] == 27 && n >= 3 && keys[1] == '[' {
			switch keys[2] {
			case 'A': // up
				move(-1)
			case 'B': // down
				move(1)
			case 'C': // right
				if isTextField(cursor) {
					s := []rune(*textFields[cursor])
					if cursors[cursor] < len(s) {
						cursors[cursor]++
					}
				}
			case 'D': // left
				if isTextField(cursor) {
					if cursors[cursor] > 0 {
						cursors[cursor]--
					}
				}
			case '3': // Delete (ESC [ 3 ~)
				if isTextField(cursor) {
					s := []rune(*textFields[cursor])
					cp := cursors[cursor]
					if cp < len(s) {
						*textFields[cursor] = string(s[:cp]) + string(s[cp+1:])
					}
				}
			}
			render()
			continue
		}

		key := keys[0]

		switch key {
		case '\t', '\n', '\r': // Tab or Enter
			if cursor == fSave {
				goto save
			}
			move(1)

		case 127, 8: // Backspace
			if isTextField(cursor) {
				s := []rune(*textFields[cursor])
				cp := cursors[cursor]
				if cp > 0 {
					*textFields[cursor] = string(s[:cp-1]) + string(s[cp:])
					cursors[cursor]--
				}
			}

		case ' ': // Space — toggle checkbox or insert into a text field.
			if isTextField(cursor) {
				cp := cursors[cursor]
				insertChar(textFields[cursor], &cp, ' ')
				cursors[cursor] = cp
			}
			if isCheckbox(cursor) {
				i := cursor - fCheckboxes
				checked[i] = !checked[i]
				errorMsg = ""
			}

		case 'a', 'A': // toggle-all (only when on a checkbox row)
			if isCheckbox(cursor) {
				allOn := true
				for _, c := range checked {
					if !c {
						allOn = false
						break
					}
				}
				for i := range checked {
					checked[i] = !allOn
				}
				errorMsg = ""
			} else if isTextField(cursor) {
				// normal character
				cp := cursors[cursor]
				insertChar(textFields[cursor], &cp, rune(key))
				cursors[cursor] = cp
			}

		default:
			if isTextField(cursor) && key >= 32 && key < 127 {
				cp := cursors[cursor]
				insertChar(textFields[cursor], &cp, rune(key))
				cursors[cursor] = cp
				// Re-resolve hints lazily while typing ref fields.
				switch cursor {
				case fBaseline:
					baselineHint = ""
				case fComparison:
					comparisonHint = ""
				case fIngest:
					ingestHint = ""
				}
			}
		}
		render()
	}

save:
	// Build benchmark selection.
	var selectedNames []string
	for i, b := range allBenchmarks {
		if checked[i] {
			selectedNames = append(selectedNames, b.Name)
		}
	}
	if len(selectedNames) == 0 {
		errorMsg = "select at least one benchmark"
		cursor = fCheckboxes
		goto retry
	}

	count, err := strconv.Atoi(strings.TrimSpace(countStr))
	if err != nil || count < 1 {
		errorMsg = "repetitions must be a positive integer"
		cursor = fCount
		goto retry
	}

	inputs.BaselineRef = strings.TrimSpace(baseline)
	inputs.ComparisonRef = strings.TrimSpace(comparison)
	ig := strings.TrimSpace(ingest)
	if ig == "comparison" {
		inputs.IngestRef = ""
	} else {
		inputs.IngestRef = ig
	}
	inputs.Count = count
	inputs.Benchtime = strings.TrimSpace(benchtime)

	if len(selectedNames) == len(allBenchmarks) {
		inputs.Benchmarks = ""
		inputs.Dataset = ""
	} else {
		inputs.Benchmarks = strings.Join(selectedNames, ",")
		inputs.Dataset = ""
	}

	inputs.defaults()
	if err := inputs.validate(); err != nil {
		errorMsg = err.Error()
		if strings.Contains(errorMsg, "benchtime") {
			cursor = fBenchtime
		} else if strings.Contains(errorMsg, "baseline_ref") {
			cursor = fBaseline
		}
		goto retry
	}
	if _, err := selectedBenchmarks(inputs); err != nil {
		errorMsg = err.Error()
		goto retry
	}

	updated := *state
	updated.Config.Inputs, updated.Plan = inputs, nil
	if err := store.save(&updated); err != nil {
		errorMsg = err.Error()
		goto retry
	}
	*state = updated
	return nil
}

// insertChar inserts r at position *cp in *s and advances *cp.
func insertChar(s *string, cp *int, r rune) {
	runes := []rune(*s)
	if *cp > len(runes) {
		*cp = len(runes)
	}
	runes = append(runes[:*cp], append([]rune{r}, runes[*cp:]...)...)
	*s = string(runes)
	*cp++
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ansiStripRe matches ANSI escape sequences so we can measure visual width.
var ansiStripRe = func() func(string) string {
	// Simple state-machine strip: ESC [ ... <final byte 0x40-0x7e>
	return func(s string) string {
		var out []rune
		runes := []rune(s)
		for i := 0; i < len(runes); i++ {
			if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
				i += 2
				for i < len(runes) && (runes[i] < 0x40 || runes[i] > 0x7e) {
					i++
				}
				continue
			}
			out = append(out, runes[i])
		}
		return string(out)
	}
}()

// padVisual left-justifies s in a field of visual width n, ignoring ANSI codes.
func padVisual(s string, n int) string {
	visual := len([]rune(ansiStripRe(s)))
	if visual >= n {
		return s
	}
	return s + strings.Repeat(" ", n-visual)
}

// filterPrint removes control characters that could corrupt the display.
func filterPrint(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
