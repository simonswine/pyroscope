package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// The Python helper runs on the supported Ubuntu image. Its stdout is only
// the two bootstrap responses, followed by agent protocol frames after exec.
// In particular it never uses buffered stdin, so it cannot consume a frame.
const bootstrapPython = `import hashlib, os, struct, sys, tempfile
root="/var/lib/macro-benchmark/agents"
os.makedirs(root,mode=0o700,exist_ok=True)
os.write(1,b"MB1\n")
def exact(n):
 out=bytearray()
 while len(out)<n:
  part=os.read(0,n-len(out))
  if not part: raise EOFError("bootstrap truncated")
  out.extend(part)
 return bytes(out)
hdr=exact(72)
size=struct.unpack(">Q",hdr[:8])[0]
digest=hdr[8:].decode("ascii")
if size<1 or size>1<<30 or len(digest)!=64 or any(c not in "0123456789abcdef" for c in digest): raise ValueError("invalid bootstrap identity")
directory=os.path.join(root,digest)
path=os.path.join(directory,"macro-benchmark")
if os.path.exists(path):
 with open(path,"rb") as f:
  h=hashlib.sha256()
  for chunk in iter(lambda:f.read(1<<20),b""): h.update(chunk)
  if h.hexdigest()!=digest or os.stat(path).st_size!=size: raise ValueError("cached agent checksum mismatch")
 os.write(1,b"C")
else:
 os.makedirs(directory,mode=0o700,exist_ok=True)
 os.write(1,b"S")
 fd,tmp=tempfile.mkstemp(prefix=".agent-",dir=directory)
 try:
  h=hashlib.sha256()
  left=size
  while left:
   chunk=exact(min(left,1<<20))
   view=memoryview(chunk)
   while view: view=view[os.write(fd,view):]
   h.update(chunk)
   left-=len(chunk)
  if h.hexdigest()!=digest: raise ValueError("agent checksum mismatch")
  os.fchmod(fd,0o700)
  os.fsync(fd)
  os.close(fd)
  fd=-1
  os.replace(tmp,path)
  dfd=os.open(directory,os.O_RDONLY)
  try: os.fsync(dfd)
  finally: os.close(dfd)
 finally:
  if fd>=0: os.close(fd)
  if os.path.exists(tmp): os.unlink(tmp)
os.execv(path,[path,"agent","--stdio"])
`

const maxAgentBinarySize = 1 << 30

// shellQuote is only used for the fixed embedded script, never for user input.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

type sshAgentConnection struct {
	in     io.WriteCloser
	out    io.ReadCloser
	cmd    *exec.Cmd
	stderr *limitedDiagnostics
	once   sync.Once
}

func (c *sshAgentConnection) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *sshAgentConnection) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *sshAgentConnection) Close() error {
	var err error
	c.once.Do(func() {
		_ = c.in.Close()
		_ = c.out.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		err = c.cmd.Wait()
	})
	return err
}

type limitedDiagnostics struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (d *limitedDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.buffer.Len() < 64<<10 {
		_, _ = d.buffer.Write(p[:min(len(p), 64<<10-d.buffer.Len())])
	}
	return len(p), nil
}
func (d *limitedDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buffer.String()
}

// connectAgent opens exactly one managed SSH process. Callers must have
// verified instance ownership and resolved its address before invoking it.
func (h remoteHost) connectAgent(ctx context.Context, binaryPath string) (*protocolClient, error) {
	file, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxAgentBinarySize {
		return nil, errors.New("invalid local agent binary")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	args := append(h.options(), "-T", "ubuntu@"+h.address, "sudo -n python3 -u -c "+shellQuote(bootstrapPython))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	conn := &sshAgentConnection{in: stdin, out: stdout, cmd: cmd, stderr: &limitedDiagnostics{}}
	cmd.Stderr = conn.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	greeting := make([]byte, 4)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return nil, fmt.Errorf("bootstrap greeting: %w (%s)", err, conn.stderr)
	}
	if string(greeting) != "MB1\n" {
		return nil, fmt.Errorf("invalid bootstrap greeting %q", greeting)
	}
	var header [72]byte
	binary.BigEndian.PutUint64(header[:8], uint64(info.Size()))
	copy(header[8:], digest)
	if _, err := conn.Write(header[:]); err != nil {
		return nil, err
	}
	var response [1]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return nil, fmt.Errorf("bootstrap response: %w (%s)", err, conn.stderr)
	}
	switch response[0] {
	case 'C':
	case 'S':
		if _, err := io.Copy(conn, file); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid bootstrap response %q", response)
	}
	success = true
	return newProtocolClient(conn), nil
}
