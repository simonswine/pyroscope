package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Machine configuration deliberately contains no benchmark or Git inputs.
func configureMachine(tty *os.File, store *stateStore, state *sessionState, startRequested ...*bool) error {
	if state.Phase != "draft" {
		return errors.New("only draft machines can be edited")
	}
	cfg := state.Config
	volume := strconv.Itoa(int(cfg.VolumeSizeGiB))
	fields := []struct {
		label string
		value *string
	}{
		{"Region (empty: discover)", &cfg.Region}, {"AMI (empty: discover)", &cfg.AMI},
		{"VPC (empty: discover)", &cfg.VPCID}, {"Subnet (empty: discover)", &cfg.SubnetID},
		{"SSH CIDR (empty: detect public IP)", &cfg.SSHCIDR}, {"SSH private key", &cfg.KeyPath},
		{"Volume GiB", &volume},
	}
	// A checkbox, followed by an explicit Save button, completes the form.
	privateField, saveField := len(fields), len(fields)+1
	cursor, scroll := 0, 0
	positions := make([]int, len(fields))
	for i, f := range fields {
		positions[i] = len(*f.value)
	}
	errorMsg := ""
	previous, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(tty.Fd()), previous) //nolint:errcheck
	fmt.Fprint(tty, "\x1b[?25l")
	for {
		width, height := terminalSize(tty)
		body := make([]string, 0, len(fields)+1)
		for i, field := range fields {
			value := terminalText(*field.value, 0)
			prefix := "  "
			if cursor == i {
				prefix = "› "
				p := positions[i]
				value = terminalText((*field.value)[:p], 0) + "\x1b[7m" + "│" + "\x1b[0m" + terminalText((*field.value)[p:], 0)
			}
			body = append(body, fmt.Sprintf("%s%-39s %s", prefix, field.label, value))
		}
		prefix, box := "  ", "[ ]"
		if cursor == privateField {
			prefix = "› "
		}
		if cfg.PrivateIP {
			box = "[✓]"
		}
		body = append(body, prefix+box+" Use private IP")
		save := "    Save"
		if cursor == saveField {
			save = "\x1b[7m  Save  \x1b[0m"
		}
		status := ""
		if errorMsg != "" {
			status = "\x1b[31m Error: " + terminalText(errorMsg, 0) + "\x1b[0m"
		}
		view, offset := renderConfigScreen(width, height, []string{" MACHINE CONFIGURATION", " " + terminalText(state.Config.RunID, 0)}, body, []string{save, " Esc cancel · ↑↓/Tab move · Enter advances / saves · Ctrl-S save & start · Space toggles", status}, cursor, scroll, cursor != saveField)
		scroll = offset
		fmt.Fprint(tty, view)
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
			continue
		}
		var buf [32]byte
		n, err := tty.Read(buf[:])
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("terminal closed")
		}
		keys := buf[:n]
		if keys[0] == 3 || keys[0] == 4 || (keys[0] == 27 && n == 1) {
			return errors.New("configuration cancelled")
		}
		if n >= 3 && keys[0] == 27 && keys[1] == '[' {
			switch keys[2] {
			case 'A':
				cursor = (cursor + saveField) % (saveField + 1)
			case 'B':
				cursor = (cursor + 1) % (saveField + 1)
			case 'Z':
				cursor = (cursor + saveField) % (saveField + 1)
			case 'C':
				if cursor < len(fields) && positions[cursor] < len(*fields[cursor].value) {
					positions[cursor]++
				}
			case 'D':
				if cursor < len(fields) && positions[cursor] > 0 {
					positions[cursor]--
				}
			}
			continue
		}
		for _, key := range keys {
			switch key {
			case 19, '\t', '\r', '\n':
				if key != 19 && cursor != saveField {
					cursor = (cursor + 1) % (saveField + 1)
					continue
				}
				if err := validateMachineForm(&cfg, volume); err != nil {
					errorMsg = err.Error()
					break
				}
				updated := *state
				updated.Config = cfg
				if err := store.save(&updated); err != nil {
					errorMsg = err.Error()
					break
				}
				*state = updated
				if key == 19 && len(startRequested) > 0 {
					*startRequested[0] = true
				}
				return nil
			case 127, 8:
				if cursor < len(fields) && positions[cursor] > 0 {
					p := positions[cursor]
					v := fields[cursor].value
					*v = (*v)[:p-1] + (*v)[p:]
					positions[cursor]--
				}
			default:
				if cursor == privateField && key == ' ' {
					cfg.PrivateIP = !cfg.PrivateIP
				}
				if cursor < len(fields) && key >= 32 && key < 127 {
					p := positions[cursor]
					v := fields[cursor].value
					*v = (*v)[:p] + string(key) + (*v)[p:]
					positions[cursor]++
				}
			}
		}
	}
}

func validateMachineForm(cfg *runConfig, volume string) error {
	size, err := strconv.ParseInt(volume, 10, 32)
	if err != nil || size <= 0 {
		return errors.New("volume must be a positive GiB value")
	}
	cfg.VolumeSizeGiB = int32(size)
	return nil
}
