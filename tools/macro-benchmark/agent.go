package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const agentRoot = "/var/lib/macro-benchmark"

type agentNode struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type agentHello struct {
	Versions        []int  `json:"versions"`
	NodeID          string `json:"node_id"`
	Initialize      bool   `json:"initialize,omitempty"`
	ControllerBuild string `json:"controller_build,omitempty"`
}

type agentGreeting struct {
	Version      int       `json:"version"`
	Node         agentNode `json:"node"`
	Capabilities []string  `json:"capabilities"`
	MaxHeader    int       `json:"max_header"`
	MaxPayload   int       `json:"max_payload"`
}

// agentServer holds the controller lease for the entire stdio session. It
// deliberately does not own worker processes: losing SSH only releases this
// lease, not the execution slot or the systemd worker.
type agentServer struct {
	root   string
	lease  *os.File
	node   agentNode
	upload *blobUpload
}

func openAgentServer(root string) (*agentServer, error) {
	if err := os.MkdirAll(filepath.Join(root, "locks"), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "locks", "control.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("controller_busy: %w", err)
	}
	return &agentServer{root: root, lease: file}, nil
}

func (s *agentServer) Close() error {
	s.abortUpload()
	return s.lease.Close()
}

func (s *agentServer) hello(h agentHello) (agentGreeting, error) {
	if !validSessionID(h.NodeID) {
		return agentGreeting{}, errors.New("invalid node identity")
	}
	supported := false
	for _, v := range h.Versions {
		if v == protocolVersion {
			supported = true
		}
	}
	if !supported {
		return agentGreeting{}, errors.New("unsupported_version: no common protocol version")
	}
	path := filepath.Join(s.root, "node.json")
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.node); err != nil {
			return agentGreeting{}, fmt.Errorf("invalid node identity: %w", err)
		}
		if s.node.Version != 1 || !validSessionID(s.node.ID) {
			return agentGreeting{}, errors.New("invalid persisted node identity")
		}
		if s.node.ID != h.NodeID {
			return agentGreeting{}, errors.New("node identity mismatch")
		}
	case os.IsNotExist(err):
		if !h.Initialize {
			return agentGreeting{}, errors.New("node identity has not been initialized")
		}
		s.node = agentNode{Version: 1, ID: h.NodeID, CreatedAt: time.Now().UTC()}
		if err := atomicJSON(path, s.node); err != nil {
			return agentGreeting{}, err
		}
	default:
		return agentGreeting{}, err
	}
	return agentGreeting{Version: protocolVersion, Node: s.node, Capabilities: []string{"describe_node", "list_runs", "get_run", "upload_blob", "prepare_run", "finalize_run", "start_run", "stop_run", "get_artifacts", "download_artifact"}, MaxHeader: maxFrameHeader, MaxPayload: maxFramePayload}, nil
}

type agentRunSummary struct {
	ID    string `json:"id"`
	Phase string `json:"phase"`
	Error string `json:"error,omitempty"`
}

func (s *agentServer) runSummary(id string) (agentRunSummary, error) {
	if !validSessionID(id) {
		return agentRunSummary{}, errors.New("invalid run ID")
	}
	status, err := loadWorkerState(filepath.Join(s.root, "runs", id, "worker"))
	if err != nil {
		return agentRunSummary{}, err
	}
	if status.Phase == "not-started" {
		intentPath := filepath.Join(s.root, "runs", id, "start.json")
		if data, err := os.ReadFile(intentPath); err == nil {
			var intent startIntent
			if err := json.Unmarshal(data, &intent); err != nil {
				return agentRunSummary{}, err
			}
			paths, _ := newRunPaths(s.root, id)
			active, err := unitActive(paths.unitName())
			if err != nil {
				return agentRunSummary{}, err
			}
			if active || time.Since(intent.RequestedAt) < 30*time.Second {
				return agentRunSummary{ID: id, Phase: "starting"}, nil
			}
			return agentRunSummary{ID: id, Phase: "interrupted", Error: "worker submission did not publish a start marker; refusing to replay"}, nil
		} else if !os.IsNotExist(err) {
			return agentRunSummary{}, err
		}
		if _, err := os.Stat(filepath.Join(s.root, "runs", id, "manifest.json")); err != nil {
			return agentRunSummary{}, err
		}
		if info, err := os.Lstat(filepath.Join(s.root, "runs", id, "bundle")); err == nil && info.IsDir() {
			return agentRunSummary{ID: id, Phase: "ready"}, nil
		} else if err != nil && !os.IsNotExist(err) {
			return agentRunSummary{}, err
		}
		return agentRunSummary{ID: id, Phase: "uploading"}, nil
	}
	if status.Phase == "running" {
		paths, err := newRunPaths(s.root, id)
		if err != nil {
			return agentRunSummary{}, err
		}
		active, err := unitActive(paths.unitName())
		if err != nil {
			return agentRunSummary{}, err
		}
		if !active {
			latest, err := loadWorkerState(paths.Worker)
			if err != nil {
				return agentRunSummary{}, err
			}
			if latest.Phase == "completed" || latest.Phase == "failed" || latest.Phase == "stopped" {
				return agentRunSummary{ID: id, Phase: latest.Phase, Error: latest.Error}, nil
			}
			return agentRunSummary{ID: id, Phase: "interrupted", Error: "worker unit stopped without publishing terminal status"}, nil
		}
	}
	if status.Phase == "interrupted" {
		// The start marker precedes status publication. Never mistake a live
		// worker for a crashed one during this small startup window.
		paths, err := newRunPaths(s.root, id)
		if err != nil {
			return agentRunSummary{}, err
		}
		output, err := exec.Command("systemctl", "show", paths.unitName(), "--property=ActiveState", "--value").Output()
		if err == nil && (strings.TrimSpace(string(output)) == "active" || strings.TrimSpace(string(output)) == "activating") {
			return agentRunSummary{ID: id, Phase: "starting"}, nil
		}
	}
	return agentRunSummary{ID: id, Phase: status.Phase, Error: status.Error}, nil
}

func (s *agentServer) listRuns() ([]agentRunSummary, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "runs"))
	if os.IsNotExist(err) {
		return []agentRunSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	runs := make([]agentRunSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validSessionID(entry.Name()) {
			return nil, errors.New("invalid run directory")
		}
		run, err := s.runSummary(entry.Name())
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

func (s *agentServer) handle(f protocolFrame) protocolFrame {
	h := frameHeader{Version: protocolVersion, Kind: "response", RequestID: f.Header.RequestID}
	respond := func(value any, err error) protocolFrame {
		if err != nil {
			code := "invalid_state"
			for _, stable := range []string{"checksum_mismatch", "insufficient_space", "node_busy", "controller_busy", "unsupported_version", "not_found", "manifest_conflict", "deadline_expired"} {
				if strings.HasPrefix(err.Error(), stable) {
					code = stable
					break
				}
			}
			h.Error = &protocolError{Code: code, Message: err.Error()}
			return protocolFrame{Header: h}
		}
		body, e := json.Marshal(value)
		if e != nil {
			h.Error = &protocolError{Code: "internal", Message: e.Error()}
		} else {
			h.Body = body
		}
		return protocolFrame{Header: h}
	}
	if f.Header.Kind == "cancel" {
		if s.upload != nil && s.upload.request.Digest == f.Header.StreamID {
			s.abortUpload()
		}
		return respond(struct{}{}, nil)
	}
	if f.Header.Kind == "chunk" {
		progress, err := s.uploadChunk(f.Header.StreamID, f.Header.Offset, f.Payload)
		return respond(progress, err)
	}
	if f.Header.Kind != "request" {
		return respond(nil, errors.New("expected request"))
	}
	switch f.Header.Method {
	case "hello":
		var request agentHello
		if err := json.Unmarshal(f.Header.Body, &request); err != nil {
			return respond(nil, err)
		}
		greeting, err := s.hello(request)
		return respond(greeting, err)
	case "describe_node":
		return respond(s.node, nil)
	case "list_runs":
		runs, err := s.listRuns()
		return respond(runs, err)
	case "get_run":
		run, err := s.runSummary(f.Header.RunID)
		return respond(run, err)
	case "prepare_run":
		var manifest runManifest
		if err := json.Unmarshal(f.Header.Body, &manifest); err != nil {
			return respond(nil, err)
		}
		if f.Header.RunID != manifest.RunID {
			return respond(nil, errors.New("manifest run identity mismatch"))
		}
		result, err := s.prepareRun(manifest)
		return respond(result, err)
	case "finalize_run":
		var request struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(f.Header.Body, &request); err != nil {
			return respond(nil, err)
		}
		return respond(struct{}{}, s.finalizeRun(f.Header.RunID, request.Digest))
	case "start_run":
		var request struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(f.Header.Body, &request); err != nil {
			return respond(nil, err)
		}
		run, err := s.startRun(f.Header.RunID, request.Digest)
		return respond(run, err)
	case "stop_run":
		run, err := s.stopRun(f.Header.RunID)
		return respond(run, err)
	case "get_artifacts":
		artifact, err := s.artifact(f.Header.RunID)
		if os.IsNotExist(err) {
			return respond([]artifactInfo{}, nil)
		}
		if err != nil {
			return respond(nil, err)
		}
		return respond([]artifactInfo{artifact}, nil)
	case "download_artifact":
		var request artifactRequest
		if err := json.Unmarshal(f.Header.Body, &request); err != nil {
			return respond(nil, err)
		}
		chunk, payload, err := s.downloadArtifact(f.Header.RunID, request)
		if err != nil {
			return respond(nil, err)
		}
		response := respond(chunk, nil)
		response.Payload = payload
		return response
	case "upload_blob":
		var request blobRequest
		if err := json.Unmarshal(f.Header.Body, &request); err != nil {
			return respond(nil, err)
		}
		progress, err := s.startBlobUpload(request)
		return respond(progress, err)
	default:
		return respond(nil, fmt.Errorf("unsupported method %q", f.Header.Method))
	}
}

// serveAgent accepts only blob uploads and read-only node/run requests.
// Run mutations are not advertised until their at-most-once guarantees exist.
func (s *agentServer) serveAgent(ctx context.Context, in io.Reader, out io.Writer) error {
	defer s.abortUpload()
	// Closing stdin on cancellation releases an idle exact-read without
	// affecting a detached worker.
	finished := make(chan struct{})
	defer close(finished)
	if closer, ok := in.(io.ReadCloser); ok {
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-finished:
			}
		}()
	}
	first, err := readProtocolFrame(in)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if first.Header.Kind != "request" || first.Header.Method != "hello" {
		return errors.New("first agent request must be hello")
	}
	reply := s.handle(first)
	if err := writeProtocolFrame(out, reply); err != nil {
		return err
	}
	if reply.Header.Error != nil {
		return errors.New(reply.Header.Error.Message)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := readProtocolFrame(in)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := writeProtocolFrame(out, s.handle(f)); err != nil {
			return err
		}
	}
}
