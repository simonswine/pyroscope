package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
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
	// The report command works locally on a downloaded archive. Agent and worker
	// remain private remote protocol commands; interactive actions live in the TUI.
	switch args[0] {
	case "agent":
		if len(args) != 2 || args[1] != "--stdio" {
			return fmt.Errorf("agent requires --stdio")
		}
		if runtime.GOOS != "linux" || os.Geteuid() != 0 {
			return fmt.Errorf("agent requires root on the disposable Linux host")
		}
		server, err := openAgentServer(agentRoot)
		if err != nil {
			return err
		}
		defer server.Close()
		return server.serveAgent(ctx, os.Stdin, os.Stdout)
	case "worker":
		return worker(ctx, args[1:])
	case "report":
		if len(args) != 3 {
			return fmt.Errorf("usage: macro-benchmark report RESULTS.tar.gz OUTPUT.html")
		}
		return generateReport(args[1], args[2])
	case "-h", "--help":
		fmt.Println("Usage: macro-benchmark [report RESULTS.tar.gz OUTPUT.html]\nInteractive controller. State: $XDG_STATE_HOME/pyroscope-macro-benchmark (default ~/.local/state/pyroscope-macro-benchmark).")
		return nil
	default:
		return fmt.Errorf("unknown command %q; launch macro-benchmark without arguments to open the TUI", args[0])
	}
}
