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
		_, _ = io.WriteString(tty, tuiDisplayWithConfirmation(width, height, root, states, selected, message, cancel != nil, confirm))
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
				case 's':
					start(states[selected], stopSession)
				case 'r':
					start(states[selected], resumeSession)
				}
				continue
			}
			if key == 'q' || key == 3 || key == 4 {
				return nil
			}
			switch key {
			case 'j':
				if selected+1 < len(states) {
					selected++
				}
			case 'k':
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
					message = "Created " + state.Config.RunID + ". p prepares without launching AWS resources."
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
			case 'e', 'p', 'r', 's', 'd', 'g':
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
					start(state, func(ctx context.Context, _ *stateStore, s *sessionState) error { return collectSession(ctx, s) })
				case 'r':
					if state.Phase == "prepared" || state.Phase == "provisioning" {
						confirm = &tuiConfirmation{action: key, runID: state.Config.RunID, title: "LAUNCH / RESUME", detail: "AWS provisioning incurs charges."}
					} else {
						start(state, resumeSession)
					}
				case 's', 'd':
					confirm = &tuiConfirmation{action: key, runID: state.Config.RunID, title: "STOP WORKER", detail: "The instance continues billing."}
					if key == 'd' {
						confirm.title = "DESTROY INSTANCE"
						confirm.detail = "Instance data will be lost. Collect results with g first."
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
	action byte
	runID  string
	title  string
	detail string
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
	const (
		reset = "\x1b[0m"
		dim   = "\x1b[2m"
		cyan  = "\x1b[1;36m"
		amber = "\x1b[33m"
	)
	type row struct{ text, style string }
	var lines []row
	add := func(text, style string) { lines = append(lines, row{text, style}) }
	add(" PYROSCOPE  /  MACRO BENCHMARK", cyan)
	add(" Persistent sessions · AWS workers · Profiling at scale", dim)
	add(strings.Repeat("─", max(1, width-1)), dim)
	add(" NEW SESSION   n configure · all benchmarks selected by default", "")
	add(fmt.Sprintf(" SESSIONS (%d)   ↑/↓ or j/k select · saved checkpoints; r refreshes", len(states)), cyan)
	selected = max(0, min(selected, len(states)-1))
	first := 0
	visible := max(1, height-21)
	if selected >= visible {
		first = selected - visible + 1
	}
	for i := first; i < len(states) && i < first+visible; i++ {
		state := states[i]
		mark, style := "  ", ""
		switch state.Phase {
		case "failed", "error":
			style = "\x1b[31m"
		case "completed", "prepared":
			style = "\x1b[32m"
		case "running", "provisioning":
			style = amber
		}
		if i == selected {
			mark, style = "› ", "\x1b[1;30;46m"
		}
		benchmarks, _ := selectedBenchmarks(state.Config.Inputs)
		ago := timeAgo(state.UpdatedAt)
		if ago != "" {
			ago = "  " + ago
		}
		add(fmt.Sprintf("%s%-13s %s  /  %d benchmarks%s", mark, state.Phase, state.Config.RunID, len(benchmarks), ago), style)
	}
	if len(states) == 0 {
		add(" No sessions yet. Press n to select benchmarks and Git refs.", dim)
	}
	if len(states) > 0 {
		s := states[selected]
		add("", "")
		add(" SELECTED SESSION", cyan)
		add(" Region    "+s.Config.Region+"    Instance  "+s.InstanceID, "")
		add(" Refs      "+s.Config.Inputs.BaselineRef+" → "+s.Config.Inputs.ComparisonRef, "")
		ingest := s.Config.Inputs.IngestRef
		if ingest == "" {
			ingest = s.Config.Inputs.ComparisonRef
		}
		add(fmt.Sprintf(" Ingest    %s · repetitions %d · benchtime %s", ingest, s.Config.Inputs.Count, s.Config.Inputs.Benchtime), "")
		add(" Results   "+filepath.Join(s.Config.Results, s.Config.RunID), dim)
		if !s.ExpiresAt.IsZero() {
			add(" Deadline  "+s.ExpiresAt.Format(time.RFC3339), amber)
		}
		if s.Error != "" {
			add(" Error     "+s.Error, "\x1b[31m")
		}
	}
	add("", "")
	status := " READY"
	if busy {
		status = " WORKING · c detaches the local operation"
	}
	add(status, cyan)
	add(" "+message, amber)
	add(" Logs  "+filepath.Join(root, "controller.log"), dim)
	add(strings.Repeat("─", max(1, width-1)), dim)
	add(" e edit  p prepare  r resume  h SSH  g collect  c detach", "")
	add(" s stop     d destroy     q quit", "")
	add(" EC2 keeps billing after quit/stop. Use d to destroy resources.", amber)
	var display strings.Builder
	display.WriteString("\x1b[H\x1b[2J")
	// Draw the confirmation over the session view rather than burying the question
	// in the status line. Keep it usable on narrow terminals too.
	panelWidth := min(70, max(1, width-1))
	panelLines := []string(nil)
	if confirm != nil {
		panelLines = []string{
			"CONFIRM: " + confirm.title,
			"Session: " + confirm.runID,
			confirm.detail,
			"Press Y to confirm · any other key cancels",
		}
	}
	panelTop := max(0, (height-1-len(panelLines)-2)/2)
	for i := 0; i < height-1 && (i < len(lines) || confirm != nil); i++ {
		line := row{}
		if i < len(lines) {
			line = lines[i]
		}
		if confirm != nil && i >= panelTop && i < panelTop+len(panelLines)+2 {
			const (
				popupBg    = "\x1b[48;5;235m" // dark grey background
				popupFg    = "\x1b[38;5;255m" // near-white text
				popupBold  = "\x1b[1m"
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
				text = terminalText(" "+raw, panelWidth)
				text += strings.Repeat(" ", max(0, panelWidth-len([]rune(text))))
				if i == panelTop+1 {
					cellStyle = popupBg + popupBold + popupFg
				} else {
					cellStyle = popupBg + popupFg
				}
			}
			display.WriteString(cellStyle + padding + text + reset + "\r\n")
			continue
		}
		display.WriteString(line.style)
		display.WriteString(terminalText(line.text, max(1, width-1)))
		display.WriteString(reset + "\r\n")
	}
	return display.String()
}
