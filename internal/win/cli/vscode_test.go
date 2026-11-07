package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robymus/fclaude/internal/win/vmssh"
)

func TestForwardEnv(t *testing.T) {
	got := forwardEnv([]string{
		"CLAUDE_CODE_ENTRYPOINT=claude-vscode",
		"CLAUDE_AGENT_SDK_VERSION=0.1.2",
		"CLAUDE_CODE_SSE_PORT=1234",
		`CLAUDE_CODE_GIT_BASH_PATH=C:\git\bash.exe`,
		`CLAUDE_CODE_TMP=c:\tmp`,
		"CLAUDE_CODE_X=c:/tmp",
		"CLAUDE_CONFIG_DIR=x",
		"PATH=/bin",
		"CLAUDE_CODE_BAD-NAME=1",
		"CLAUDE_CODE_EMIT_TOOL_USE_SUMMARIES=true",
	})
	want := "CLAUDE_AGENT_SDK_VERSION=0.1.2 CLAUDE_CODE_EMIT_TOOL_USE_SUMMARIES=true CLAUDE_CODE_ENTRYPOINT=claude-vscode"
	if strings.Join(got, " ") != want {
		t.Errorf("forwardEnv = %q\nwant %q", got, want)
	}
}

func TestSplitAddDirs(t *testing.T) {
	rest, dirs := splitAddDirs([]string{"--verbose", `--add-dir=c:\dev\a`, "--add-dir", `c:\dev\b`, "--debug", "--add-dir"})
	if strings.Join(rest, " ") != "--verbose --debug --add-dir" || strings.Join(dirs, " ") != `c:\dev\a c:\dev\b` {
		t.Errorf("rest %q dirs %q", rest, dirs)
	}
}

func TestVSCodeHelperSkipsMount(t *testing.T) {
	ta := newTestApp(t)
	ta.vm.running = true
	ta.guest.run = func(_ string, in io.Reader, out, _ io.Writer) error {
		b, _ := io.ReadAll(in)
		_, _ = io.WriteString(out, `{"loggedIn":true,"echo":"`+strings.TrimSpace(string(b))+`"}`)
		return &vmssh.ExitError{Status: 4}
	}
	ta.In = strings.NewReader("ping\n")
	code, err := ta.VSCode(context.Background(), `C:\Program Files\Microsoft VS Code`, []string{"auth", "status", "--json"},
		[]string{"CLAUDE_CODE_ENTRYPOINT=claude-vscode"})
	if err != nil || code != 4 {
		t.Fatalf("code %d err %v", code, err)
	}
	remote := ta.guest.cmds[len(ta.guest.cmds)-1]
	if remote != loginEnv+" cd '/home/fclaude' && exec env 'CLAUDE_CODE_ENTRYPOINT=claude-vscode' claude 'auth' 'status' '--json'" {
		t.Errorf("remote %q", remote)
	}
	if ta.stdout.String() != `{"loggedIn":true,"echo":"ping"}` {
		t.Errorf("stdout %q", ta.stdout.String())
	}
	if len(ta.vm.calls) != 0 {
		t.Errorf("helper touched shares: %v", ta.vm.calls)
	}
	if st, _ := ta.Store.List(); len(st) != 0 {
		t.Errorf("helper took a lease: %+v", st)
	}
}

func TestVSCodeOutsideShareRoot(t *testing.T) {
	windowsOnly(t)
	ta := newTestApp(t)
	ta.vm.running = true
	_, err := ta.VSCode(context.Background(), ta.Layout.Home, []string{"--output-format", "stream-json"}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot run in the fclaude VM") {
		t.Fatalf("err %v", err)
	}
}

func TestVSCodeSessionRewritesAndShares(t *testing.T) {
	windowsOnly(t)
	ta := newTestApp(t)
	ta.vm.running = true
	proj := filepath.Join(ta.Cfg.ShareRoot, "proj")
	extra := filepath.Join(ta.Cfg.ShareRoot, "extra")
	for _, d := range []string{proj, extra} {
		_ = os.MkdirAll(d, 0o700)
	}
	ta.guest.reply = func(string) (string, error) { return "", nil } // shares already fine
	// VS Code's fsPath spelling: lower-case drive.
	lc := func(p string) string { return strings.ToLower(p[:1]) + p[1:] }
	cwd := lc(proj)
	var gotIn string
	ta.guest.run = func(_ string, in io.Reader, out, _ io.Writer) error {
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			gotIn += sc.Text() + "\n"
			_, _ = io.WriteString(out, `{"type":"system","cwd":"/home/fclaude/proj"}`+"\n")
		}
		return nil
	}
	in := `{"type":"user","message":{"content":"look at ` + strings.ReplaceAll(cwd, `\`, `\\`) + `\\main.go"}}` + "\n"
	ta.In = strings.NewReader(in)
	code, err := ta.VSCode(context.Background(), cwd,
		[]string{"--output-format", "stream-json", "--add-dir=" + lc(extra), `--add-dir=C:\elsewhere`},
		[]string{"CLAUDE_CODE_ENTRYPOINT=claude-vscode"})
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, ta.stderr.String())
	}
	if !strings.Contains(gotIn, `look at /home/fclaude/proj/main.go`) {
		t.Errorf("stdin not mapped to the VM: %q", gotIn)
	}
	wantOut := `{"type":"system","cwd":"` + strings.ReplaceAll(cwd, `\`, `\\`) + `"}` + "\n"
	if ta.stdout.String() != wantOut {
		t.Errorf("stdout %q\nwant   %q", ta.stdout.String(), wantOut)
	}
	remote := ""
	for _, c := range ta.guest.cmds {
		if strings.Contains(c, "exec env") {
			remote = c
		}
	}
	for _, want := range []string{"cd '/home/fclaude/proj'", "exec env 'CLAUDE_CODE_ENTRYPOINT=claude-vscode' claude '--output-format' 'stream-json' '--add-dir=/home/fclaude/extra'"} {
		if !strings.Contains(remote, want) {
			t.Errorf("remote %q lacks %q", remote, want)
		}
	}
	if strings.Contains(remote, "elsewhere") || !strings.Contains(ta.stderr.String(), `not sharing the extra folder C:\elsewhere`) {
		t.Errorf("outside --add-dir not dropped: %q\n%s", remote, ta.stderr.String())
	}
	if st, _ := ta.Store.List(); len(st) != 0 {
		t.Errorf("not released: %+v", st)
	}
}
