package install

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/robymus/fclaude/internal/win/vmssh"
)

// WaitProvisioned returns the production Deps.Provision: wait for the VM's
// sshd, pin its host key (trust on first use, over the host-only network the
// VM was just created on), then follow provisioning until
// /var/lib/fclaude/.provisioned appears, showing the latest cloud-init output
// line as progress.
func WaitProvisioned(l Layout, user string, ui UI, timeout time.Duration) func(context.Context, VMInfo) error {
	return func(ctx context.Context, info VMInfo) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cfg := vmssh.Config{Addr: info.SSHAddr(), User: user, KeyFile: l.Key(user), KnownHosts: l.KnownHosts(), Timeout: 10 * time.Second}

		ui.Info("Waiting for the VM to boot...")
		var key ssh.PublicKey
		for {
			k, err := vmssh.FetchHostKey(ctx, cfg)
			if err == nil {
				key = k
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("the VM did not answer ssh at %s: %w", cfg.Addr, err)
			case <-time.After(3 * time.Second):
			}
		}
		if err := vmssh.WritePin(cfg.KnownHosts, cfg.Addr, key); err != nil {
			return err
		}
		ui.Info("VM is up (host key %s pinned). Provisioning...", key.Type())

		c, err := vmssh.Dial(ctx, cfg)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		const probe = "test -f /var/lib/fclaude/.provisioned && echo FCLAUDE-PROVISIONED || sudo -n tail -n1 /var/log/cloud-init-output.log 2>/dev/null"
		last := ""
		for {
			out, err := c.Output(ctx, probe)
			if err == nil {
				line := strings.TrimSpace(out)
				if line == "FCLAUDE-PROVISIONED" {
					return nil
				}
				if line != "" && line != last {
					ui.Info("  %s", truncate(line, 100))
					last = line
				}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("provisioning did not finish within %v", timeout)
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
