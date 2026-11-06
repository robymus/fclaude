package install

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/robymus/fclaude/internal/win/vmw"
)

// ProductionDeps wires Setup to the real system.
func ProductionDeps(home, version string, ui UI) (Deps, error) {
	self, err := os.Executable()
	if err != nil {
		return Deps{}, err
	}
	l := Layout{Home: home}
	d := Deps{
		Layout:   l,
		UI:       ui,
		Locate:   vmw.Locate,
		Networks: vmw.ReadNetworks,
		NewVM:    func(in vmw.Install) VM { return vmw.Client{R: vmw.ExecRunner{Path: in.Vmrun()}} },
		Vdisk:    func(in vmw.Install) vmw.Runner { return vmw.ExecRunner{Path: in.Vdiskmanager()} },
		Fetch:    HTTPFetch,
		Self:     self,
		Version:  version,
		Rand:     rand.Reader,
		HaveSSHClient: func() bool {
			return fileExists(filepath.Join(os.Getenv("SystemRoot"), "System32", "OpenSSH", "ssh.exe"))
		},
		FirewallPresent: func() (bool, error) {
			out, err := PowerShell(FirewallCheckScript())
			if err != nil {
				return false, err
			}
			n, err := strconv.Atoi(strings.TrimSpace(out))
			return err == nil && n >= 3, err
		},
		Elevate:         func(args []string) error { return RunElevated(self, args) },
		AddPath:         AddToUserPath,
		RemovePath:      RemoveFromUserPath,
		ScheduleRemoval: ScheduleRemoval,
		Provision:       WaitProvisioned(l, "fclaude", ui, 30*time.Minute),
		Now:             time.Now,
	}
	if appData := os.Getenv("APPDATA"); appData != "" && fileExists(filepath.Join(appData, "Code")) {
		d.VSCodeSettings = filepath.Join(appData, "Code", "User", "settings.json")
	}
	return d, nil
}

// ElevatedStep is `fclaude-setup install-elevated --subnets A,B`: the one part
// of setup that runs as administrator (through UAC, without a visible
// console). Its output goes to <home>\elevated.log for diagnosis.
func ElevatedStep(home string, args []string) error {
	fs := flag.NewFlagSet("install-elevated", flag.ContinueOnError)
	subnets := fs.String("subnets", "", "comma-separated VMware subnets (CIDR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logPath := filepath.Join(home, "elevated.log")
	logf := func(format string, a ...any) {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		_, _ = fmt.Fprintf(f, time.Now().Format(time.RFC3339)+" "+format+"\n", a...)
	}
	if !IsElevated() {
		err := errors.New("install-elevated must run as administrator (setup starts it through UAC)")
		logf("%v", err)
		return err
	}
	script, err := FirewallScript(strings.Split(*subnets, ",")...)
	if err != nil {
		logf("%v", err)
		return err
	}
	out, err := PowerShell(script)
	logf("firewall for %s: err=%v output=%q", *subnets, err, out)
	return err
}

// ElevatedUninstallStep is `fclaude uninstall-elevated`: remove the firewall
// rules (run through UAC by Uninstall).
func ElevatedUninstallStep(home string) error {
	if !IsElevated() {
		return errors.New("uninstall-elevated must run as administrator (uninstall starts it through UAC)")
	}
	out, err := PowerShell(FirewallRemoveScript())
	if f, ferr := os.OpenFile(filepath.Join(home, "elevated.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil {
		_, _ = fmt.Fprintf(f, "%s firewall removal: err=%v output=%q\n", time.Now().Format(time.RFC3339), err, out)
		_ = f.Close()
	}
	return err
}
