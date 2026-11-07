package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("FCW_HELPER") == "claude" {
		_, _ = os.Stdout.WriteString("native claude ran\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMode(t *testing.T) {
	cases := map[string]string{
		`C:\Users\x\.fclaude\bin\fclaude-vscode.exe`:    "fclaude-vscode",
		`FCLAUDE-SETUP.EXE`:                             "fclaude-setup",
		`/usr/local/bin/fclaude-capture`:                "fclaude-capture",
		`fclaude.exe`:                                   "fclaude",
		`fclaude-setup (1).exe`:                         "fclaude-setup", // browser-renamed download
		`C:\Users\x\Downloads\fclaude-setup-v0.1.0.exe`: "fclaude-setup",
		`fclaude-setupx.exe`:                            "fclaude-setup",
		`fclaude-vscode (1).exe`:                        "fclaude",
		`fclaude-win`:                                   "fclaude",
	}
	for in, want := range cases {
		if got := mode(in); got != want {
			t.Errorf("mode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"fclaude.exe", "--version"}, nil, &out, &out); code != 0 || !strings.Contains(out.String(), "dev") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"fclaude-setup.exe", "-h"}, nil, &out, &out); code != 0 || !strings.Contains(out.String(), "Personalities") {
		t.Fatalf("help: %d %q", code, out.String())
	}
}

func TestVSCodeWrapperOwnsItsArgs(t *testing.T) {
	t.Setenv("FCLAUDE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	// The VS Code wrapper must not treat --version as its own flag: every arg
	// belongs to claude. Without an installation it says so.
	if code := run([]string{"fclaude-vscode.exe", "--version"}, nil, &out, &errb); code != 1 || out.Len() != 0 {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
	if !strings.Contains(errb.String(), "run fclaude-setup") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestCLIWithoutInstall(t *testing.T) {
	t.Setenv("FCLAUDE_HOME", t.TempDir())
	var errb bytes.Buffer
	if code := run([]string{"fclaude.exe", "--status"}, nil, &bytes.Buffer{}, &errb); code != 1 || !strings.Contains(errb.String(), "run fclaude-setup") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	var out bytes.Buffer
	if code := run([]string{"fclaude.exe", "--help"}, nil, &out, &out); code != 0 || !strings.Contains(out.String(), "grow-disk") {
		t.Fatalf("help: %d %q", code, out.String())
	}
}

func TestCapture(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FCLAUDE_CAPTURE_DIR", dir)
	t.Setenv("FCW_HELPER", "claude")
	var out, errb bytes.Buffer
	code := run([]string{"fclaude-capture.exe", testExe(t), "-test.run=^$"}, strings.NewReader(""), &out, &errb)
	if code != 0 || out.String() != "native claude ran\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("logs: %v", logs)
	}
	b, _ := os.ReadFile(logs[0])
	if !bytes.Contains(b, []byte(`"k":"exit"`)) {
		t.Fatalf("log incomplete: %s", b)
	}
}

func TestCaptureUsage(t *testing.T) {
	var errb bytes.Buffer
	if code := run([]string{"fclaude-capture.exe"}, nil, &bytes.Buffer{}, &errb); code != 2 || !strings.Contains(errb.String(), "usage") {
		t.Fatalf("%d %q", code, errb.String())
	}
}

func TestCaptureUnloggableStillRuns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(file, nil, 0o600)
	t.Setenv("FCLAUDE_CAPTURE_DIR", filepath.Join(file, "sub")) // MkdirAll fails
	t.Setenv("FCW_HELPER", "claude")
	var out bytes.Buffer
	if code := run([]string{"fclaude-capture.exe", testExe(t), "-test.run=^$"}, strings.NewReader(""), &out, &bytes.Buffer{}); code != 0 || out.Len() == 0 {
		t.Fatalf("session broken by logging failure: %d", code)
	}
}

// testExe is this test binary's absolute path. os.Args[0] can be a bare
// relative name (e.g. when launched through WSL interop), which os/exec
// refuses to run.
func testExe(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
