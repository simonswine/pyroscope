package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func ensureSSHKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, "pyroscope-macro-benchmark")
		if err != nil {
			return nil, err
		}
		data = pem.EncodeToMemory(block)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("SSH private key %s must be a regular file readable only by its owner", path)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse SSH private key: %w", err)
	}
	public := ssh.MarshalAuthorizedKey(signer.PublicKey())
	if existing, err := os.ReadFile(path + ".pub"); err == nil {
		if strings.TrimSpace(string(existing)) != strings.TrimSpace(string(public)) {
			return nil, fmt.Errorf("SSH public key does not match private key")
		}
	} else if os.IsNotExist(err) {
		if err := os.WriteFile(path+".pub", public, 0644); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	return public, nil
}

type remoteHost struct{ address, key, knownHosts string }

func (h remoteHost) options() []string {
	return []string{"-i", h.key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + h.knownHosts}
}

func (h remoteHost) ssh(ctx context.Context, command string) error {
	args := append(h.options(), "ubuntu@"+h.address, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (h remoteHost) output(ctx context.Context, command string) ([]byte, error) {
	args := append(h.options(), "ubuntu@"+h.address, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func (h remoteHost) copy(ctx context.Context, source, dest string, upload bool) error {
	if upload {
		dest = "ubuntu@" + h.address + ":" + dest
	} else {
		source = "ubuntu@" + h.address + ":" + source
	}
	args := append(h.options(), "-r", source, dest)
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (h remoteHost) wait(ctx context.Context) error {
	for {
		if err := h.ssh(ctx, "true"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for SSH: %w", ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}
