package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const defaultPublishRepo = "simonswine/pyroscope-macro-benchmark"

var publishName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var publishRepo = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

func publishCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(out)
	repo := fs.String("repo", defaultPublishRepo, "GitHub owner/repository (must already have Pages configured)")
	name := fs.String("name", "", "unique report name (defaults to the run directory name)")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: macro-benchmark publish [--repo OWNER/REPO] [--name NAME] RUN_DIRECTORY_OR_RESULTS.tar.gz\nPublishes reports and native profiles publicly; review the evidence before publishing.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("publish requires one results archive or run directory")
	}
	if !publishRepo.MatchString(*repo) {
		return errors.New("repo must be OWNER/REPO")
	}
	input, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	info, err := os.Stat(input)
	if err != nil {
		return err
	}
	archive := input
	if info.IsDir() {
		archive = filepath.Join(input, "results.tar.gz")
	}
	if *name == "" {
		if info.IsDir() {
			*name = filepath.Base(input)
		} else {
			*name = filepath.Base(filepath.Dir(input))
		}
	}
	if !publishName.MatchString(*name) {
		return errors.New("name must be 1–128 letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	// Generate into a clean staging directory: never upload arbitrary run files,
	// the results archive, credentials, or the large offline HTML variant.
	stage, err := os.MkdirTemp("", "macro-benchmark-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := generateReport(archive, filepath.Join(stage, "index.html")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stage, "index-local.html")); err != nil {
		return err
	}
	if err := filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() >= 100*1024*1024 {
			return fmt.Errorf("report file %s exceeds GitHub's 100 MiB file limit", entry.Name())
		}
		return nil
	}); err != nil {
		return err
	}
	var metadata struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := publishAPI(ctx, "repos/"+*repo, &metadata); err != nil {
		return err
	}
	var pages struct {
		HTMLURL   string `json:"html_url"`
		BuildType string `json:"build_type"`
		Source    struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"source"`
	}
	if err := publishAPI(ctx, "repos/"+*repo+"/pages", &pages); err != nil {
		return fmt.Errorf("repository must already have GitHub Pages configured: %w", err)
	}
	branch := pages.Source.Branch
	if branch == "" {
		branch = metadata.DefaultBranch
	}
	if pages.Source.Path != "" && pages.Source.Path != "/" {
		return errors.New("only repository-root Pages sites are supported")
	}
	if branch == "" || pages.HTMLURL == "" {
		return errors.New("GitHub returned incomplete Pages configuration")
	}
	site, err := url.Parse(pages.HTMLURL)
	if err != nil || site.Scheme != "https" || site.Host == "" {
		return errors.New("GitHub returned an invalid Pages URL")
	}
	reportURL := strings.TrimRight(pages.HTMLURL, "/") + "/" + *name + "/"
	if err := publishGitRepository(ctx, "https://github.com/"+*repo+".git", branch, stage, *name, reportURL); err != nil {
		return err
	}
	fmt.Fprintln(out, reportURL)
	fmt.Fprintln(out, "Committed. GitHub Pages deployment may take a few minutes.")
	return nil
}

func publishAPI(ctx context.Context, endpoint string, result any) error {
	cmd := exec.CommandContext(ctx, "gh", "api", endpoint)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh api %s: %w: %s", endpoint, err, strings.TrimSpace(stderr.String()))
	}
	return json.Unmarshal(data, result)
}

// Use gh's credential helper without changing the user's global Git settings.
func publishGit(ctx context.Context, dir string, args ...string) error {
	options := []string{"-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}
	cmd := exec.CommandContext(ctx, "git", append(options, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(data)))
	}
	return nil
}

func publishGitRepository(ctx context.Context, remote, branch, stage, name, reportURL string) error {
	if !publishName.MatchString(name) {
		return errors.New("invalid report name")
	}
	work, err := os.MkdirTemp("", "macro-benchmark-publish-git-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	if err := publishGit(ctx, work, "clone", "--depth=1", "--single-branch", "--branch", branch, "--", remote, "repo"); err != nil {
		return err
	}
	root := filepath.Join(work, "repo")
	destination := filepath.Join(root, name)
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("report %q already exists; choose a new --name", name)
	}
	// Index changes are prepared before committing. Refuse unfamiliar formats
	// rather than silently replacing a hand-maintained catalogue.
	if err := updatePublishIndexes(root, name, reportURL); err != nil {
		return err
	}
	if err := os.CopyFS(destination, os.DirFS(stage)); err != nil {
		return err
	}
	if _, err := readPublishIndex(filepath.Join(root, ".nojekyll")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".nojekyll"), nil, 0644); err != nil {
		return err
	}
	if err := publishGit(ctx, root, "add", "--", name, "index.html", "README.md", ".nojekyll"); err != nil {
		return err
	}
	if err := publishGit(ctx, root, "-c", "user.name=macro-benchmark", "-c", "user.email=macro-benchmark@users.noreply.github.com", "commit", "-m", "Publish macro benchmark report "+name); err != nil {
		return err
	}
	// No force push: a concurrent publication must never be overwritten. The
	// caller can safely retry; nothing was published if this atomic push fails.
	if err := publishGit(ctx, root, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return fmt.Errorf("publication failed (possibly a concurrent update); retry the command: %w", err)
	}
	return nil
}

func updatePublishIndexes(root, name, reportURL string) error {
	indexPath := filepath.Join(root, "index.html")
	index, err := readPublishIndex(indexPath)
	if err != nil {
		return err
	}
	if len(index) == 0 {
		index = []byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Pyroscope macro-benchmark reports</title><h1>Pyroscope macro-benchmark reports</h1><ul></ul></html>\n")
	}
	if !bytes.Contains(index, []byte("<ul>")) {
		return errors.New("index.html has no <ul> report list; refusing to overwrite it")
	}
	entry := "<li><a href=\"" + name + "/\">" + html.EscapeString(name) + "</a></li>"
	index = bytes.Replace(index, []byte("<ul>"), []byte("<ul>"+entry), 1)
	readmePath := filepath.Join(root, "README.md")
	readme, err := readPublishIndex(readmePath)
	if err != nil {
		return err
	}
	if len(readme) == 0 {
		readme = []byte("# Pyroscope macro-benchmark reports\n")
	}
	readme = append(readme, []byte("\n- ["+name+"]("+reportURL+") — [source]("+name+"/index.html)\n")...)
	if err := os.WriteFile(indexPath, index, 0644); err != nil {
		return err
	}
	return os.WriteFile(readmePath, readme, 0644)
}

func readPublishIndex(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular index %s", path)
	}
	return os.ReadFile(path)
}
