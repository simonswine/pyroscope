package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return runTUI(ctx)
	}
	// These commands are a private protocol for the remote worker and observer,
	// not alternative controller entry points. All user actions live in the TUI.
	switch args[0] {
	case "worker":
		return worker(ctx, args[1:])
	case "worker-status":
		state, err := loadWorkerState(workerDirectory)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(state)
	case "execute":
		return execute(ctx)
	case "observe":
		return observe(ctx, args[1:])
	case "now-ms":
		fmt.Println(time.Now().UnixMilli())
		return nil
	case "-h", "--help":
		fmt.Println("Usage: macro-benchmark\nInteractive controller. State: $XDG_STATE_HOME/pyroscope-macro-benchmark (default ~/.local/state/pyroscope-macro-benchmark).")
		return nil
	default:
		return fmt.Errorf("unknown command %q; launch macro-benchmark without arguments to open the TUI", args[0])
	}
}
