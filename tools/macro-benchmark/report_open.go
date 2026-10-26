package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func openSessionReport(state sessionState) error {
	dir := filepath.Join(state.Config.Results, state.Config.RunID)
	filename := filepath.Join(dir, "report-local.html")
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		if err := generateReport(filepath.Join(dir, "results.tar.gz"), filepath.Join(dir, "report.html")); err != nil {
			return fmt.Errorf("collect results with g first, or retry report generation: %w", err)
		}
	} else if err != nil {
		return err
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	target := (&url.URL{Scheme: "file", Path: absolute}).String()
	if err := exec.Command(opener, target).Run(); err != nil {
		return fmt.Errorf("open report %s: %w", filename, err)
	}
	return nil
}
