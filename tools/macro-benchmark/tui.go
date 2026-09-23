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
	var confirm byte
	var confirmID string
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
		renderTUI(tty, root, states, selected, message, cancel != nil)
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
		key := buf[0]
		if key == 'q' || key == 3 || key == 4 {
			return nil
		}
		if confirm != 0 {
			requested := confirm
			confirm = 0
			if key != 'Y' {
				message = "Cancelled."
				continue
			}
			if len(states) == 0 || states[selected].Config.RunID != confirmID {
				message = "Selection changed; try again."
				continue
			}
			if requested == 'd' {
				start(states[selected], destroySession)
			} else if requested == 's' {
				start(states[selected], stopSession)
			} else {
				start(states[selected], resumeSession)
			}
			continue
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
					confirm, confirmID = key, state.Config.RunID
					message = "Launch/resume AWS provisioning? This incurs charges. Press Y to confirm; any other key cancels."
				} else {
					start(state, resumeSession)
				}
			case 's', 'd':
				confirm, confirmID = key, state.Config.RunID
				message = "Stop worker (instance continues billing)? Press Y to confirm; any other key cancels."
				if key == 'd' {
					message = "DESTROY instance and its data? Collect results with g first. Press Y to confirm."
				}
			}
		}
	}
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

func renderTUI(out *os.File, root string, states []sessionState, selected int, message string, busy bool) {
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil || width < 1 || height < 1 {
		width, height = 100, 30
	}
	_, _ = io.WriteString(out, tuiDisplay(width, height, root, states, selected, message, busy))
}

func tuiDisplay(width, height int, root string, states []sessionState, selected int, message string, busy bool) string {
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
	add(fmt.Sprintf(" SESSIONS (%d)   j/k select · saved checkpoints; r refreshes", len(states)), cyan)
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
		add(fmt.Sprintf("%s%-13s %s  /  %d benchmarks", mark, state.Phase, state.Config.RunID, len(benchmarks)), style)
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
	for i, line := range lines {
		if i >= height-1 {
			break
		}
		display.WriteString(line.style)
		display.WriteString(terminalText(line.text, max(1, width-1)))
		display.WriteString(reset + "\r\n")
	}
	return display.String()
}
