package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Keep the UI on /dev/tty so subprocess output cannot corrupt the display.
// Logs and every generated local artifact live under the state directory.
func runTUI(ctx context.Context) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("the benchmark controller requires an interactive terminal: %w", err)
	}
	defer tty.Close()
	root, err := stateDirectory()
	if err != nil {
		return err
	}
	store, err := openStateStore(root)
	if err != nil {
		return err
	}
	defer store.Close()
	store.transportCtx = ctx
	if _, err := store.list(); err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Join(root, "controller.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	stdout, stderr, logger := os.Stdout, os.Stderr, log.Writer()
	os.Stdout, os.Stderr = output, output
	log.SetOutput(output)
	defer func() { os.Stdout, os.Stderr = stdout, stderr; log.SetOutput(logger) }()
	previous, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(tty.Fd()), previous)
	fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(tty, "\x1b[?25h\x1b[?1049l")

	selected := 0
	detailOffset := 0
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	lastSpin := time.Now()
	message := "Saved checkpoints shown. r reconciles with AWS; it never replays a started worker."
	var confirm *tuiConfirmation
	var keys tuiKeys
	var cancel context.CancelFunc
	var done chan error
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	start := func(state sessionState, action func(context.Context, *stateStore, *sessionState) error) {
		workCtx, stop := context.WithCancel(ctx)
		cancel = stop
		done = make(chan error, 1)
		message = "Working on " + state.Config.RunID + ". c detaches; q quits without destroying AWS resources."
		go func() { done <- sessionAction(workCtx, store, &state, action) }()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			cancel()
			cancel, done = nil, nil
			if err != nil && !errors.Is(err, context.Canceled) {
				message = err.Error()
			} else {
				message = "Checkpoint saved. Remote resources remain until explicitly destroyed."
			}
		default:
		}
		states, err := store.list()
		if err != nil {
			return err
		}
		if selected >= len(states) {
			selected = len(states) - 1
		}
		if selected < 0 {
			selected = 0
		}
		width, height := terminalSize(tty)
		if time.Since(lastSpin) >= spin.Spinner.FPS {
			spin, _ = spin.Update(spin.Tick())
			lastSpin = time.Now()
		}
		_, _ = io.WriteString(tty, tuiDashboard(width, height, root, states, selected, message, cancel != nil, confirm, spin.View(), detailOffset))
		// Darwin's poll reports POLLNVAL for /dev/tty; select works on both
		// macOS controllers and Linux without a permanently blocked reader.
		var readable unix.FdSet
		fd := int(tty.Fd())
		if fd >= unix.FD_SETSIZE {
			return errors.New("terminal file descriptor exceeds select capacity")
		}
		readable.Set(fd)
		timeout := unix.NsecToTimeval(int64(250 * time.Millisecond))
		ready, err := unix.Select(fd+1, &readable, nil, nil, &timeout)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if ready == 0 {
			continue
		}
		var buf [32]byte
		n, err := tty.Read(buf[:])
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		for _, key := range keys.feed(buf[:n]) {
			if confirm != nil {
				requested := confirm
				confirm = nil
				if key != 'Y' {
					message = "Cancelled."
					continue
				}
				if len(states) == 0 || states[selected].Config.RunID != requested.runID {
					message = "Selection changed; try again."
					continue
				}
				switch requested.action {
				case 'd':
					start(states[selected], destroySession)
				case 'a':
					if err := store.archive(requested.runID); err != nil {
						message = "Archive: " + err.Error()
					} else {
						message = "Session archived locally. AWS resources were not changed."
					}
				case 's':
					start(states[selected], stopSession)
				case 'r':
					start(states[selected], resumeSession)
				case 'R':
					child, err := store.createRerun(states[selected], requested.benchmarks)
					if err != nil {
						message = err.Error()
						break
					}
					message = "Created child session " + child.Config.RunID + ". Preparing without replay."
					start(*child, func(ctx context.Context, store *stateStore, child *sessionState) error {
						if err := prepareRerunSession(ctx, store, child); err != nil {
							return err
						}
						return followManaged(ctx, store, child)
					})
				}
				continue
			}
			if key == 'q' || key == 3 || key == 4 {
				return nil
			}
			switch key {
			case ']':
				detailOffset++
			case '[':
				detailOffset = max(0, detailOffset-1)
			case 'j':
				detailOffset = 0
				if selected+1 < len(states) {
					selected++
				}
			case 'k':
				detailOffset = 0
				if selected > 0 {
					selected--
				}
			case 'h':
				if len(states) == 0 {
					continue
				}
				if err := interactiveSSH(ctx, tty, previous, states[selected]); err != nil {
					message = "SSH: " + err.Error()
				} else {
					message = "SSH closed. Remote worker and AWS resources are unchanged."
				}
			case 'c':
				if cancel != nil {
					cancel()
					message = "Detaching local operation; remote worker is unaffected."
				}
			case 'n':
				if cancel != nil {
					message = "Detach the active operation with c first."
					continue
				}
				state, err := store.create("")
				if err != nil {
					return err
				}
				if err := configureRun(tty, store, state); err != nil {
					message = "Draft created; configuration not changed: " + err.Error()
				} else {
					message = "Created session " + state.Config.RunID + ". p prepares without launching AWS resources."
				}
				updated, err := store.list()
				if err != nil {
					return err
				}
				for i := range updated {
					if updated[i].Config.RunID == state.Config.RunID {
						selected = i
					}
				}
			case 'e', 'p', 'r', 'R', 's', 'd', 'g', 'a':
				if cancel != nil {
					message = "Detach the active operation with c first."
					continue
				}
				if len(states) == 0 {
					continue
				}
				state := states[selected]
				switch key {
				case 'e':
					if state.Phase != "draft" {
						message = "Only draft sessions can be edited."
					} else if err := configureRun(tty, store, &state); err != nil {
						message = err.Error()
					} else {
						message = "Selection saved. p resolves refs and prepares the bundle."
					}
				case 'p':
					start(state, prepareSession)
				case 'g':
					start(state, func(ctx context.Context, _ *stateStore, s *sessionState) error { return collectSession(ctx, store, s) })
				case 'R':
					if state.Kind == "rerun" {
						message = "Select the storage owner instead."
						break
					}
					if state.Plan == nil || (state.Phase != "completed" && state.Phase != "failed") {
						message = "Only terminal owners with an ingestion-ready record can be rerun."
						break
					}
					names := make([]string, 0, len(state.Plan.Benchmarks))
					for _, b := range state.Plan.Benchmarks {
						names = append(names, b.Name)
					}
					selection := strings.Join(names, ",")
					if _, err := selectedBenchmarks(inputsConfig{Benchmarks: selection}); err != nil {
						selection, err = promptRerunBenchmarks(tty, state)
						if err != nil {
							message = err.Error()
							break
						}
					}
					candidate := state.Config.Inputs
					candidate.Benchmarks = selection
					benchmarks, err := selectedBenchmarks(candidate)
					if err != nil {
						message = err.Error()
						break
					}
					allowed := map[string]bool{}
					for _, d := range state.Plan.Datasets {
						allowed[d.Name] = true
					}
					valid := true
					for _, b := range benchmarks {
						if !allowed[b.Dataset] {
							message = "Dataset not replayed: " + b.Dataset
							valid = false
							break
						}
					}
					if !valid {
						break
					}
					tenants := make([]string, 0, len(state.Plan.Datasets))
					for _, d := range state.Plan.Datasets {
						tenants = append(tenants, d.Name+"="+d.Tenant)
					}
					confirm = &tuiConfirmation{action: 'R', runID: state.Config.RunID, benchmarks: selection, title: "RERUN WITHOUT REPLAY", detail: fmt.Sprintf("Owner %s on %s\nBenchmarks: %s → %s\nTenants: %s\nCommits baseline/comparison/ingest: %.12s / %.12s / %.12s\nNo replay: existing storage reused; no profiles pushed.", state.Config.RunID, state.InstanceID, strings.Join(names, ","), selection, strings.Join(tenants, ","), state.Plan.Baseline.Commit, state.Plan.Comparison.Commit, state.Plan.Ingest.Commit)}
				case 'r':
					if state.Kind == "rerun" {
						start(state, resumeSession)
						break
					}
					if state.Phase == "prepared" || state.Phase == "provisioning" || state.Phase == "uploading" || state.Phase == "ready" {
						confirm = &tuiConfirmation{action: key, runID: state.Config.RunID, title: "LAUNCH / RESUME", detail: "AWS provisioning incurs charges."}
					} else {
						start(state, resumeSession)
					}
				case 'a':
					if state.Phase != "draft" && state.Phase != "destroyed" {
						message = "Only draft or destroyed sessions can be archived."
					} else {
						confirm = &tuiConfirmation{action: key, runID: state.Config.RunID, title: "ARCHIVE SESSION", detail: "Hide this local checkpoint; AWS is not modified."}
					}
				case 's', 'd':
					confirm = &tuiConfirmation{action: key, runID: state.Config.RunID, title: "STOP WORKER", detail: "The instance continues billing."}
					if key == 'd' {
						confirm.title = "DESTROY INSTANCE"
						confirm.detail = "Instance and all child rerun storage will be lost. Collect results with g first."
					}
				}
			}
			// Require a fresh keystroke after the popup has been drawn.
			if confirm != nil {
				break
			}
		}
	}
}

type tuiConfirmation struct {
	action     byte
	runID      string
	title      string
	detail     string
	benchmarks string
}

// Decode arrow sequences even when the terminal splits them across reads.
// An arrow is treated exactly like j/k on the session list.
type tuiKeys struct{ escape int }

func (k *tuiKeys) feed(input []byte) []byte {
	var result []byte
	for _, b := range input {
		switch k.escape {
		case 1:
			k.escape = 0
			if b == '[' || b == 'O' {
				k.escape = 2
			}
		case 2:
			k.escape = 0
			switch b {
			case 'A':
				result = append(result, 'k')
			case 'B':
				result = append(result, 'j')
			}
		default:
			if b == 27 {
				k.escape = 1
			} else {
				result = append(result, b)
			}
		}
	}
	return result
}

func terminalText(s string, width int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	runes := []rune(s)
	if width > 0 && len(runes) > width {
		return string(runes[:width])
	}
	return s
}

// interactiveSSH lends the real terminal to OpenSSH, including cooked input and
// signals. Background controller operations continue logging to controller.log.
func interactiveSSH(ctx context.Context, tty *os.File, previous *term.State, state sessionState) (err error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	host, err := sessionHost(lookupCtx, &state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(host.knownHosts), 0700); err != nil {
		return err
	}
	if err := term.Restore(int(tty.Fd()), previous); err != nil {
		return err
	}
	fmt.Fprint(tty, "\x1b[0m\x1b[?25h\x1b[?1049l")
	defer func() {
		_, rawErr := term.MakeRaw(int(tty.Fd()))
		err = errors.Join(err, rawErr)
		fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l")
	}()
	cmd := exec.CommandContext(ctx, "ssh", append(host.options(), "-t", "ubuntu@"+host.address)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	return cmd.Run()
}

// timeAgo returns a human-readable relative time string, e.g. "3 min ago".
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 h ago"
		}
		return fmt.Sprintf("%d h ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		weeks := int(d.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	}
}

func terminalSize(out *os.File) (int, int) {
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil || width < 1 || height < 1 {
		return 100, 30
	}
	return width, height
}

func tuiDisplay(width, height int, root string, states []sessionState, selected int, message string, busy bool) string {
	return tuiDisplayWithConfirmation(width, height, root, states, selected, message, busy, nil)
}

func tuiDisplayWithConfirmation(width, height int, root string, states []sessionState, selected int, message string, busy bool, confirm *tuiConfirmation) string {
	return tuiDisplayWithConfirmationAndSpinner(width, height, root, states, selected, message, busy, confirm, spinner.MiniDot.Frames[0])
}

func activePhase(phase string) bool {
	switch phase {
	case "preparing", "provisioning", "uploading", "starting", "running", "stopping", "destroying":
		return true
	default:
		return false
	}
}

// Use black text on light backgrounds for every phase badge. Explicit
// background colours keep the labels readable across terminal themes.
func phaseBadgeStyle(phase string) string {
	switch phase {
	case "failed", "interrupted", "error", "destroying":
		return "\x1b[38;5;16;48;5;210m" // black on light red
	case "completed", "prepared", "ready":
		return "\x1b[38;5;16;48;5;157m" // black on light green
	case "running":
		return "\x1b[38;5;16;48;5;159m" // black on light cyan
	case "provisioning":
		return "\x1b[38;5;16;48;5;153m" // black on light blue
	case "uploading", "stopping":
		return "\x1b[38;5;16;48;5;183m" // black on light purple
	case "draft", "destroyed", "stopped":
		return "\x1b[38;5;16;48;5;252m" // black on light grey
	default:
		return "\x1b[38;5;16;48;5;229m" // black on light yellow
	}
}

// colourPhaseCell overrides selection styling only within the phase cell.
func colourPhaseCell(line, phase string, width int) string {
	cell := ansi.Strip(ansi.Cut(line, 0, width))
	cell += strings.Repeat(" ", max(0, width-ansi.StringWidth(cell)))
	return phaseBadgeStyle(phase) + cell + "\x1b[0m" + ansi.Cut(line, width, ansi.StringWidth(line))
}

func tuiDisplayWithConfirmationAndSpinner(width, height int, root string, states []sessionState, selected int, message string, busy bool, confirm *tuiConfirmation, frame string) string {
	return tuiDashboard(width, height, root, states, selected, message, busy, confirm, frame, 0)
}

func tuiDashboard(width, height int, root string, states []sessionState, selected int, message string, busy bool, confirm *tuiConfirmation, frame string, detailOffset int) string {
	const (
		reset = "\x1b[0m"
		dim   = "\x1b[2m"
		cyan  = "\x1b[1;36m"
		amber = "\x1b[33m"
	)
	type row struct {
		text, style string
		trusted     bool
	}
	width, height = max(1, width), max(1, height)
	screenWidth := width
	wide := width >= 120
	// Reserve the header and footer before allocating any content space.
	const headerHeight, footerHeight = 3, 8
	bodyHeight := max(0, height-1-headerHeight-footerHeight)
	var lines []row
	add := func(text, style string) { lines = append(lines, row{text: text, style: style}) }
	addTableLine := func(text string) { lines = append(lines, row{text: text, trusted: true}) }
	add(" PYROSCOPE  /  MACRO BENCHMARK", cyan)
	add(strings.Repeat("─", max(1, width-1)), dim)
	add(fmt.Sprintf(" SESSIONS (%d)", len(states)), cyan)
	if wide {
		width = (screenWidth - 3) / 2
	}
	bodyStart := len(lines)
	sessionHeight := bodyHeight
	if !wide {
		// Stable detail allocation, including optional error/deadline fields.
		sessionHeight = max(2, bodyHeight-12)
	}
	selected = max(0, min(selected, len(states)-1))
	if len(states) > 0 {
		// Bubbles owns the table's cursor, viewport and column truncation. The
		// rest of the existing TUI (confirmation and input form) stays unchanged.
		available := max(4, width-2)
		phaseWidth := max(1, min(16, available/4))
		benchWidth := max(1, min(7, available/8))
		ageWidth := max(1, min(13, available/5))
		idWidth := max(1, available-phaseWidth-benchWidth-ageWidth)
		columns := []table.Column{{Title: "PHASE", Width: phaseWidth}, {Title: "SESSION ID", Width: idWidth}, {Title: "BENCH", Width: benchWidth}, {Title: "UPDATED", Width: ageWidth}}
		rows := make([]table.Row, 0, len(states))
		for i, state := range states {
			benchmarks, _ := selectedBenchmarks(state.Config.Inputs)
			phase := "  " + state.Phase
			if activePhase(state.Phase) {
				phase = frame + " " + state.Phase
			}
			marker := " "
			if i == selected {
				marker = "›"
			}
			// Reserve one cell for selection so the spinner and text never move.
			phase = marker + phase
			rows = append(rows, table.Row{terminalText(phase, 0), terminalText(state.Config.RunID, 0), fmt.Sprintf("%d", len(benchmarks)), terminalText(timeAgo(state.UpdatedAt), 0)})
		}
		visibleRows := max(1, sessionHeight-1)
		firstRow := max(0, selected-visibleRows+1)
		lastRow := min(len(rows), firstRow+visibleRows)
		view := table.New(table.WithColumns(columns), table.WithRows(rows[firstRow:lastRow]), table.WithHeight(visibleRows+1), table.WithWidth(available))
		styles := table.DefaultStyles()
		styles.Header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
		styles.Cell = lipgloss.NewStyle()
		// Keep the selected row light with black text; phase badges use the
		// same foreground, so neither selection nor terminal themes wash it out.
		styles.Selected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#d6dce7"))
		view.SetStyles(styles)
		view.MoveDown(selected - firstRow)
		for i, line := range strings.Split(view.View(), "\n") {
			// Colour by row identity, not text matching: even truncated phase
			// names get the correct colour. Leave the header and blank rows alone.
			if i > 0 && firstRow+i-1 < lastRow {
				line = colourPhaseCell(line, states[firstRow+i-1].Phase, phaseWidth)
			}
			addTableLine(line)
		}
	}
	if len(states) == 0 {
		add(" No sessions yet. Press n to select benchmarks and Git refs.", dim)
	}
	detailStart := len(lines)
	if len(states) > 0 {
		s := states[selected]
		add("", "")
		add(" SELECTED SESSION   [/] scroll", cyan)
		ingest := s.Config.Inputs.IngestRef
		if ingest == "" {
			ingest = s.Config.Inputs.ComparisonRef
		}
		details := []table.Row{
			{"Region", terminalText(s.Config.Region, 0)},
			{"Instance", terminalText(s.InstanceID, 0)},
			{"Refs", terminalText(s.Config.Inputs.BaselineRef+" → "+s.Config.Inputs.ComparisonRef, 0)},
			{"Ingest", terminalText(ingest, 0)},
			{"Measurement", terminalText(fmt.Sprintf("%d repetitions · benchtime %s", s.Config.Inputs.Count, s.Config.Inputs.Benchtime), 0)},
			{"Results", terminalText(filepath.Join(s.Config.Results, s.Config.RunID), 0)},
		}
		if s.Kind == "rerun" {
			details = append(details, table.Row{"Storage owner", terminalText(s.StorageRunID, 0)})
		}
		if !s.ExpiresAt.IsZero() {
			details = append(details, table.Row{"Deadline", s.ExpiresAt.Format(time.RFC3339)})
		}
		if s.Error != "" {
			details = append(details, table.Row{"Error", terminalText(s.Error, 0)})
		}
		fieldWidth := min(15, max(1, (width-2)/3))
		detailTable := table.New(table.WithColumns([]table.Column{
			{Title: "FIELD", Width: fieldWidth},
			{Title: "VALUE", Width: max(1, width-2-fieldWidth)},
		}), table.WithRows(details), table.WithHeight(len(details)+1), table.WithWidth(max(2, width-2)))
		detailStyles := table.DefaultStyles()
		lightBg := lipgloss.Color("#e5e7eb")
		detailStyles.Header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lightBg)
		detailStyles.Cell = lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(lightBg)
		detailStyles.Selected = detailStyles.Cell // Read-only table; no selected detail row.
		detailTable.SetStyles(detailStyles)
		for _, line := range strings.Split(detailTable.View(), "\n") {
			addTableLine(line)
		}
	}
	detailEnd := len(lines)
	width = screenWidth
	add("", "")
	status := " READY"
	if busy {
		status = " WORKING · c detaches the local operation"
	}
	add(status, cyan)
	add(" "+message, amber)
	add(" Logs  "+filepath.Join(root, "controller.log"), dim)
	add(strings.Repeat("─", max(1, width-1)), dim)
	add(" e edit  p prepare  r resume  R rerun  h SSH  g collect  c detach", "")
	add(" n new  ↑/↓ j/k select  s stop  d destroy  a archive  q quit", "")
	add(" EC2 keeps billing after quit/stop. Use d to destroy resources.", amber)
	// Compose fixed regions rather than truncating a variable-length page.
	// ANSI-aware clipping also prevents wide Unicode cells from wrapping.
	renderRow := func(r row, cells int) string {
		text := r.text
		if !r.trusted {
			text = terminalText(text, 0)
		}
		return r.style + ansi.Truncate(text, max(0, cells), "") + reset
	}
	fit := func(rows []row, count int) []row {
		out := make([]row, max(0, count))
		copy(out, rows)
		return out
	}
	header := append([]row(nil), lines[:bodyStart]...)
	sessions := lines[bodyStart:detailStart]
	details := lines[detailStart:detailEnd]
	detailHeight := bodyHeight
	if !wide {
		detailHeight = max(0, bodyHeight-min(sessionHeight, bodyHeight))
	}
	// Clamp scrolling to the final full page; the heading stays visible.
	if len(details) > 2 && detailHeight > 2 {
		offset := min(max(0, detailOffset), max(0, len(details)-detailHeight))
		details = append(append([]row(nil), details[:2]...), details[2+offset:]...)
	}
	footer := append([]row(nil), lines[detailEnd:]...)
	body := make([]row, bodyHeight)
	if wide {
		leftWidth := (screenWidth - 3) / 2
		left, right := fit(sessions, bodyHeight), fit(details, bodyHeight)
		for i := range body {
			l := renderRow(left[i], leftWidth)
			l += strings.Repeat(" ", max(0, leftWidth-ansi.StringWidth(l)))
			body[i] = row{text: l + dim + " │ " + reset + renderRow(right[i], screenWidth-1-leftWidth-3), trusted: true}
		}
	} else {
		n := min(sessionHeight, bodyHeight)
		copy(body, fit(sessions, n))
		copy(body[n:], details)
	}
	lines = append(header, body...)
	lines = append(lines, footer...)
	if height-1 < headerHeight+footerHeight {
		// Compact terminals prioritize status and controls over the body.
		lines = append(fit(header, min(2, height-1)), footer[1:]...)
	}
	var display strings.Builder
	display.WriteString("\x1b[H\x1b[2J")
	// Draw the confirmation over the session view rather than burying the question
	// in the status line. Keep it usable on narrow terminals too.
	panelWidth := min(70, max(1, width-1))
	panelLines := []string(nil)
	if confirm != nil {
		panelLines = []string{"CONFIRM: " + confirm.title, "Session: " + confirm.runID}
		panelLines = append(panelLines, strings.Split(confirm.detail, "\n")...)
		panelLines = append(panelLines, "Press Y to confirm · any other key cancels")
	}
	panelTop := max(0, (height-1-len(panelLines)-2)/2)
	for i := 0; i < height-1 && (i < len(lines) || confirm != nil); i++ {
		line := row{}
		if i < len(lines) {
			line = lines[i]
		}
		if confirm != nil && i >= panelTop && i < panelTop+len(panelLines)+2 {
			const (
				popupBg     = "\x1b[48;5;235m" // dark grey background
				popupFg     = "\x1b[38;5;255m" // near-white text
				popupBold   = "\x1b[1m"
				popupBorder = "\x1b[38;5;214m" // amber border
			)
			padding := strings.Repeat(" ", max(0, (width-1-panelWidth)/2))
			var text, cellStyle string
			if i == panelTop || i == panelTop+len(panelLines)+1 {
				// Border rows
				text = strings.Repeat("─", panelWidth)
				cellStyle = popupBg + popupBorder
			} else {
				// Content rows – first row is the title, rest are plain
				raw := panelLines[i-panelTop-1]
				text = ansi.Truncate(terminalText(" "+raw, 0), panelWidth, "")
				text += strings.Repeat(" ", max(0, panelWidth-ansi.StringWidth(text)))
				if i == panelTop+1 {
					cellStyle = popupBg + popupBold + popupFg
				} else {
					cellStyle = popupBg + popupFg
				}
			}
			display.WriteString(cellStyle + padding + text + reset + "\r\n")
			continue
		}
		display.WriteString(renderRow(line, width-1))
		display.WriteString(reset + "\r\n")
	}
	return display.String()
}
