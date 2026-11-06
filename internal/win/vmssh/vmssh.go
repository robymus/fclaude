// Package vmssh is the in-process ssh client the Windows tooling uses to reach
// the VM: mount commands, status queries and the VS Code wrapper's stdio
// session (which needs no PTY, so no ssh.exe).
//
// The host key is pinned. Setup captures it once on the first boot
// (FetchHostKey + WritePin, trust-on-first-use over the host-only network the
// VM was just created on); every later connection must match it exactly —
// stricter than the Linux host's StrictHostKeyChecking=no.
package vmssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config describes one VM account.
type Config struct {
	Addr       string        // host:port, e.g. 192.168.201.10:22
	User       string        // fclaude or maint
	KeyFile    string        // unencrypted OpenSSH private key
	KnownHosts string        // pin file written by WritePin
	Timeout    time.Duration // dial + handshake; default 10s
	KeepAlive  time.Duration // keepalive interval; default 15s, <0 disables
}

func (c Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

func signer(keyFile string) (ssh.Signer, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("vmssh: %s: %w", keyFile, err)
	}
	return s, nil
}

// ErrHostKeyMismatch means the VM presented a key other than the pinned one.
var ErrHostKeyMismatch = errors.New("vmssh: VM host key does not match the pinned key")

// Client is a connected session factory with a background keepalive.
type Client struct {
	c    *ssh.Client
	once sync.Once
	stop chan struct{}
}

// Dial connects with strict host-key checking against cfg.KnownHosts.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	hk, err := knownhosts.New(cfg.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("vmssh: host key pin: %w", err)
	}
	check := func(host string, remote net.Addr, key ssh.PublicKey) error {
		if err := hk(host, remote, key); err != nil {
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) {
				return fmt.Errorf("%w (%s)", ErrHostKeyMismatch, cfg.KnownHosts)
			}
			return err
		}
		return nil
	}
	c, err := dial(ctx, cfg, check)
	if err != nil {
		return nil, err
	}
	cl := &Client{c: c, stop: make(chan struct{})}
	if ka := cfg.KeepAlive; ka >= 0 {
		if ka == 0 {
			ka = 15 * time.Second
		}
		go cl.keepalive(ka)
	}
	return cl, nil
}

func dial(ctx context.Context, cfg Config, hostKey ssh.HostKeyCallback) (*ssh.Client, error) {
	s, err := signer(cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	sc := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(s)},
		HostKeyCallback: hostKey,
		Timeout:         cfg.timeout(),
	}
	d := net.Dialer{Timeout: cfg.timeout()}
	conn, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	// Bound the handshake too: a VM that accepts TCP but never answers (still
	// booting) must not hang the caller.
	_ = conn.SetDeadline(time.Now().Add(cfg.timeout()))
	cc, chans, reqs, err := ssh.NewClientConn(conn, cfg.Addr, sc)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}

// keepalive closes the connection after three unanswered keepalives, so a VM
// that died (or a host that slept) surfaces as an error instead of a hang.
func (cl *Client) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-cl.stop:
			return
		case <-t.C:
		}
		done := make(chan error, 1)
		go func() { _, _, err := cl.c.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				_ = cl.Close()
				return
			}
			missed = 0
		case <-time.After(every):
			if missed++; missed >= 3 {
				_ = cl.Close()
				return
			}
		case <-cl.stop:
			return
		}
	}
}

// Close ends the connection and the keepalive.
func (cl *Client) Close() error {
	var err error
	cl.once.Do(func() {
		close(cl.stop)
		err = cl.c.Close()
	})
	return err
}

// ExitError is a remote command that ran and exited non-zero.
type ExitError struct {
	Cmd    string
	Status int
	Stderr string // captured by Output only
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("vmssh: %q exited %d", e.Cmd, e.Status)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

// Run executes cmd without a PTY, wiring the given streams (nil = none). A
// cancelled ctx closes the session; sshd then hangs up the remote process.
func (cl *Client) Run(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	s, err := cl.c.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	s.Stdin, s.Stdout, s.Stderr = stdin, stdout, stderr
	if err := s.Start(cmd); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = s.Signal(ssh.SIGTERM)
		_ = s.Close()
		return ctx.Err()
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Cmd: cmd, Status: ee.ExitStatus()}
	}
	return err
}

// Output runs cmd and returns its stdout; a non-zero exit is an *ExitError
// carrying stderr.
func (cl *Client) Output(ctx context.Context, cmd string) (string, error) {
	var out, errb strings.Builder
	err := cl.Run(ctx, cmd, nil, &out, &errb)
	var ee *ExitError
	if errors.As(err, &ee) {
		ee.Stderr = errb.String()
	}
	return out.String(), err
}

// FetchHostKey connects once accepting any host key and returns it, after
// authenticating with cfg.KeyFile (so a pinned key always belongs to a VM our
// key can log into). Setup calls this on the freshly created VM, over the
// host-only network, then WritePin.
func FetchHostKey(ctx context.Context, cfg Config) (ssh.PublicKey, error) {
	var got ssh.PublicKey
	c, err := dial(ctx, cfg, func(_ string, _ net.Addr, key ssh.PublicKey) error { got = key; return nil })
	if err != nil {
		return nil, err
	}
	_ = c.Close()
	return got, nil
}

// WritePin writes the known_hosts pin file for addr (atomic replace).
func WritePin(path, addr string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key) + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(line), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
