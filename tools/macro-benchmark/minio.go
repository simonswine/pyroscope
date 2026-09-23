package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func downloadMinio(ctx context.Context, url, expected, destination string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if request.URL.Scheme != "https" {
		return errors.New("MinIO download requires HTTPS")
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" {
			return errors.New("unsafe or excessive download redirects")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download MinIO: HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(destination)
		}
	}()
	hash := sha256.New()
	const limit = 512 << 20
	count, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if count > limit {
		return errors.New("MinIO download exceeds 512 MiB")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected) {
		return errors.New("MinIO release SHA256 mismatch")
	}
	if strings.HasSuffix(request.URL.Path, ".tar.gz") {
		archive := destination + ".tar.gz"
		if err := os.Rename(destination, archive); err != nil {
			return err
		}
		defer os.Remove(archive)
		if err := extractMinio(archive, destination); err != nil {
			return err
		}
	}
	return linuxAMD64(destination)
}

// Extract only the regular minio executable; never materialize archive paths.
func extractMinio(archive, destination string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 1<<30))
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(header.Name) != "minio" {
			continue
		}
		if found || header.Typeflag != tar.TypeReg || !filepath.IsLocal(header.Name) || header.Size > 512<<20 {
			return errors.New("invalid or duplicate MinIO executable in archive")
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, reader)
		closeErr := out.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("archive contains no regular minio executable")
	}
	return nil
}

type managedProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	file *os.File
}

func startProcess(name string, args, env []string, logPath string) (*managedProcess, error) {
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = file, file
	if err := cmd.Start(); err != nil {
		_ = file.Close()
		return nil, err
	}
	p := &managedProcess{cmd: cmd, done: make(chan struct{}), file: file}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *managedProcess) stop(ctx context.Context) error {
	select {
	case <-p.done:
		return errors.Join(p.err, p.file.Close())
	default:
	}
	signalErr := p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
		// SIGINT is intentional; preserve log-close failures, not signal exit codes.
		return errors.Join(signalErr, p.file.Close())
	case <-ctx.Done():
		killErr := p.cmd.Process.Kill()
		<-p.done
		return errors.Join(ctx.Err(), killErr, p.file.Close())
	}
}

func waitMinio(ctx context.Context, p *managedProcess, url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case <-p.done:
			return fmt.Errorf("MinIO exited before readiness: %v", p.err)
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/minio/health/ready", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Create the fresh bucket through the real S3 API, signed with local credentials.
// This does not use the AWS credential chain or contact AWS S3.
func createBucket(ctx context.Context, endpoint, bucket, access, secret string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/"+bucket, nil)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(nil)
	payloadHash := hex.EncodeToString(hash[:])
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	credentials := aws.Credentials{AccessKeyID: access, SecretAccessKey: secret}
	if err := v4.NewSigner().SignHTTP(ctx, credentials, request, payloadHash, "s3", "us-east-1", time.Now()); err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("create MinIO bucket: HTTP %d", response.StatusCode)
	}
	return nil
}
