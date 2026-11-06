package vmssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testServer is a minimal in-process sshd: public-key auth for one key, and
// "exec" requests handled by a tiny command set.
type testServer struct {
	addr     string
	hostKey  ssh.Signer
	ln       net.Listener
	sessions atomic.Int32
}

func newKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, pem.EncodeToMemory(blk)
}

func startServer(t *testing.T, clientKey ssh.PublicKey, silentKeepalive bool) *testServer {
	t.Helper()
	hk, _ := newKey(t)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), clientKey.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(hk)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &testServer{addr: ln.Addr().String(), hostKey: hk, ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(c, cfg, silentKeepalive)
		}
	}()
	return srv
}

func (s *testServer) serve(c net.Conn, cfg *ssh.ServerConfig, silentKeepalive bool) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	go func() {
		for r := range reqs {
			if silentKeepalive {
				continue // never answer: simulates a wedged VM
			}
			_ = r.Reply(false, nil) // any reply proves liveness
		}
	}()
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		s.sessions.Add(1)
		go func() {
			defer func() { _ = ch.Close() }()
			for r := range creqs {
				if r.Type != "exec" {
					_ = r.Reply(false, nil)
					continue
				}
				_ = r.Reply(true, nil)
				cmd := string(r.Payload[4:])
				status := uint32(0)
				switch {
				case cmd == "cat":
					_, _ = io.Copy(ch, ch)
				case strings.HasPrefix(cmd, "echo "):
					_, _ = io.WriteString(ch, strings.TrimPrefix(cmd, "echo ")+"\n")
				case cmd == "fail":
					_, _ = io.WriteString(ch.Stderr(), "mount: no such device\n")
					status = 32
				case cmd == "sleep":
					time.Sleep(5 * time.Second)
				}
				var b [4]byte
				binary.BigEndian.PutUint32(b[:], status)
				_, _ = ch.SendRequest("exit-status", false, b[:])
				return
			}
		}()
	}
}

type fixture struct {
	srv *testServer
	cfg Config
}

func setup(t *testing.T, silentKeepalive bool) fixture {
	t.Helper()
	dir := t.TempDir()
	cs, pemKey := newKey(t)
	keyFile := filepath.Join(dir, "fclaude")
	if err := os.WriteFile(keyFile, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, cs.PublicKey(), silentKeepalive)
	cfg := Config{Addr: srv.addr, User: "fclaude", KeyFile: keyFile, KnownHosts: filepath.Join(dir, "known_hosts"), Timeout: 3 * time.Second, KeepAlive: -1}
	return fixture{srv, cfg}
}

func pin(t *testing.T, f fixture) {
	t.Helper()
	k, err := FetchHostKey(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.Marshal(), f.srv.hostKey.PublicKey().Marshal()) {
		t.Fatal("fetched the wrong host key")
	}
	if err := WritePin(f.cfg.KnownHosts, f.cfg.Addr, k); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedRunAndOutput(t *testing.T) {
	f := setup(t, false)
	pin(t, f)
	ctx := context.Background()
	c, err := Dial(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if out, err := c.Output(ctx, "echo hi"); err != nil || out != "hi\n" {
		t.Errorf("Output = %q %v", out, err)
	}
	var out bytes.Buffer
	if err := c.Run(ctx, "cat", strings.NewReader(`{"type":"user"}`+"\n"), &out, io.Discard); err != nil || out.String() != `{"type":"user"}`+"\n" {
		t.Errorf("stdio = %q %v", out.String(), err)
	}
	_, err = c.Output(ctx, "fail")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Status != 32 || !strings.Contains(err.Error(), "no such device") {
		t.Errorf("exit error = %v", err)
	}
}

func TestHostKeyMismatch(t *testing.T) {
	f := setup(t, false)
	other, _ := newKey(t)
	if err := WritePin(f.cfg.KnownHosts, f.cfg.Addr, other.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(context.Background(), f.cfg); !errors.Is(err, ErrHostKeyMismatch) {
		t.Errorf("err = %v, want ErrHostKeyMismatch", err)
	}
}

func TestDialErrors(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()
	if _, err := Dial(ctx, f.cfg); err == nil {
		t.Error("dial without a pin file must fail")
	}
	pin(t, f)
	bad := f.cfg
	bad.KeyFile = filepath.Join(t.TempDir(), "missing")
	if _, err := Dial(ctx, bad); err == nil {
		t.Error("missing key accepted")
	}
	if err := os.WriteFile(bad.KeyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, bad); err == nil || !strings.Contains(err.Error(), bad.KeyFile) {
		t.Errorf("garbage key: %v", err)
	}
	wrong := f.cfg
	_, other := newKey(t)
	wrong.KeyFile = filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(wrong.KeyFile, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, wrong); err == nil {
		t.Error("unauthorized key accepted")
	}
	if _, err := FetchHostKey(ctx, wrong); err == nil {
		t.Error("FetchHostKey must authenticate")
	}
	closed := f.cfg
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed.Addr = ln.Addr().String()
	_ = ln.Close()
	if _, err := Dial(ctx, closed); err == nil {
		t.Error("closed port accepted")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	// A VM that accepts TCP but never speaks ssh (still booting).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
		}
	}()
	f := setup(t, false)
	f.cfg.Addr, f.cfg.Timeout = ln.Addr().String(), 300*time.Millisecond
	start := time.Now()
	if _, err := FetchHostKey(context.Background(), f.cfg); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("handshake not bounded: %v after %v", err, time.Since(start))
	}
}

func TestRunCancel(t *testing.T) {
	f := setup(t, false)
	pin(t, f)
	c, err := Dial(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.Run(ctx, "sleep", nil, nil, nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Errorf("cancel: %v after %v", err, time.Since(start))
	}
}

func TestKeepalive(t *testing.T) {
	// Answered keepalives keep the connection; a silent peer gets it closed.
	f := setup(t, false)
	pin(t, f)
	f.cfg.KeepAlive = 50 * time.Millisecond
	c, err := Dial(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := c.Output(context.Background(), "echo alive"); err != nil {
		t.Errorf("healthy connection dropped: %v", err)
	}
	_ = c.Close()
	_ = c.Close() // idempotent

	s := setup(t, true)
	pin(t, s)
	s.cfg.KeepAlive = 50 * time.Millisecond
	c, err = Dial(context.Background(), s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Output(context.Background(), "echo x"); err != nil {
			return // closed by the keepalive, as intended
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("connection to a silent peer was never closed")
}

func TestWritePinError(t *testing.T) {
	k, _ := newKey(t)
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePin(filepath.Join(f, "sub", "known_hosts"), "1.2.3.4:22", k.PublicKey()); err == nil {
		t.Error("pin under a file accepted")
	}
}
