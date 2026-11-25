package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func publishSessionConfirmation(state sessionState) (*tuiConfirmation, error) {
	if state.Kind == "machine" {
		return nil, fmt.Errorf("select a session to publish")
	}
	if !validSessionID(state.Config.RunID) {
		return nil, fmt.Errorf("invalid session ID")
	}
	archive := filepath.Join(state.Config.Results, state.Config.RunID, "results.tar.gz")
	info, err := os.Stat(archive)
	if err != nil {
		return nil, fmt.Errorf("collect results with g before publishing: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("results archive is not a regular file")
	}
	return &tuiConfirmation{
		action: 'P', runID: state.Config.RunID, title: "PUBLISH REPORT PUBLICLY",
		detail: fmt.Sprintf("Destination: %s\nReport: %s\nUploads the report and native profiles from collected local results.\nProfiles can expose labels, symbols, and machine information.\nExisting report names cannot be replaced. Requires gh authentication.", defaultPublishRepo, state.Config.RunID),
	}, nil
}

func publishSessionReport(ctx context.Context, state sessionState) (string, error) {
	if _, err := publishSessionConfirmation(state); err != nil {
		return "", err
	}
	var output bytes.Buffer
	dir := filepath.Join(state.Config.Results, state.Config.RunID)
	if err := publishCommand(ctx, []string{"--name", state.Config.RunID, dir}, &output); err != nil {
		return "", err
	}
	reportURL, _, _ := strings.Cut(output.String(), "\n")
	return "Published: " + reportURL + " (Pages deployment may take a few minutes)", nil
}
