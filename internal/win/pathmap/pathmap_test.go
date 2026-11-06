package pathmap

import (
	"testing"
)

func newMapper(t *testing.T) *Mapper {
	t.Helper()
	m, err := New(`C:\dev`, "/home/fclaude")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNewRejects(t *testing.T) {
	for _, c := range [][2]string{{`C:\`, "/home/fclaude"}, {`dev`, "/home/fclaude"}, {`C:\dev`, "/"}, {`C:\dev`, "rel"}} {
		if _, err := New(c[0], c[1]); err == nil {
			t.Errorf("New(%q, %q) should fail", c[0], c[1])
		}
	}
}

func TestToVMPath(t *testing.T) {
	m := newMapper(t)
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`C:\dev\app\src\x.ts`, "/home/fclaude/app/src/x.ts", true},
		{`c:/DEV/app`, "/home/fclaude/app", true},
		{`C:\dev\\app\\x`, "/home/fclaude/app/x", true},
		{`C:\dev\app\`, "/home/fclaude/app/", true},
		{`\\?\C:\dev\app`, "/home/fclaude/app", true},
		{`C:\dev\my app\a b.ts`, "/home/fclaude/my app/a b.ts", true},
		{`C:\dev\Zoë\ü.ts`, "/home/fclaude/Zoë/ü.ts", true},
		{`file:///c%3A/dev/app/x.ts`, "file:///home/fclaude/app/x.ts", true},
		{`file:///C:/dev/my%20app`, "file:///home/fclaude/my%20app", true},
		// not mapped
		{`C:\dev`, `C:\dev`, false},                       // the root itself
		{`C:\dev\.claude\x`, `C:\dev\.claude\x`, false},   // dot top-level
		{`C:\devices\x`, `C:\devices\x`, false},           // sibling with common prefix
		{`D:\dev\app`, `D:\dev\app`, false},               // other drive
		{`C:\Windows\x`, `C:\Windows\x`, false},           // outside root
		{`/home/fclaude/app`, `/home/fclaude/app`, false}, // already a VM path
		{`app\x.ts`, `app\x.ts`, false},                   // relative
		{`file:///c%3A/Windows`, `file:///c%3A/Windows`, false},
		{`file:///%zz`, `file:///%zz`, false},
		{`hello`, `hello`, false},
	}
	for _, c := range cases {
		got, ok := m.ToVMPath(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ToVMPath(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestToWinPath(t *testing.T) {
	m := newMapper(t)
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"/home/fclaude/app/src/x.ts", `C:\dev\app\src\x.ts`, true},
		{"/home/fclaude/app/", `C:\dev\app\`, true},
		{"/home/fclaude/app/./b/../c", `C:\dev\app\c`, true},
		{"file:///home/fclaude/my%20app/x.ts", "file:///c%3A/dev/my%20app/x.ts", true},
		// not mapped
		{"/home/fclaude", "/home/fclaude", false},
		{"/home/fclaude/.claude/plans/p.md", "/home/fclaude/.claude/plans/p.md", false},
		{"/home/fclaude/../etc/passwd", "/home/fclaude/../etc/passwd", false},
		{"/home/fclaudex/a", "/home/fclaudex/a", false},
		{"/tmp/x", "/tmp/x", false},
		{`C:\dev\app`, `C:\dev\app`, false},
	}
	for _, c := range cases {
		got, ok := m.ToWinPath(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ToWinPath(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	m := newMapper(t)
	for _, win := range []string{`C:\dev\app\x.ts`, `C:\dev\a b\c`, `C:\dev\Zoë`} {
		vm, ok := m.ToVMPath(win)
		if !ok {
			t.Fatalf("ToVMPath(%q) not mapped", win)
		}
		back, ok := m.ToWinPath(vm)
		if !ok || back != win {
			t.Errorf("round trip %q -> %q -> %q", win, vm, back)
		}
	}
}

func TestToVMText(t *testing.T) {
	m := newMapper(t)
	cases := []struct{ in, want string }{
		{`open C:\dev\app\x.ts please`, `open /home/fclaude/app/x.ts please`},
		{`see c:/dev/app/x.ts:12.`, `see /home/fclaude/app/x.ts:12.`},
		{"`C:\\dev\\app` and (C:\\dev\\lib\\y.go)", "`/home/fclaude/app` and (/home/fclaude/lib/y.go)"},
		{`edit "C:\dev\app\a.ts", "C:\dev\b"`, `edit "/home/fclaude/app/a.ts", "/home/fclaude/b"`},
		{`file C:\dev\app\x.ts.`, `file /home/fclaude/app/x.ts.`},
		{`url file:///c%3A/dev/app/x`, `url file:///home/fclaude/app/x`},
		// untouched
		{`C:\devices\x and XC:\dev\a`, `C:\devices\x and XC:\dev\a`},
		{`root C:\dev only`, `root C:\dev only`},
		{`nothing here`, `nothing here`},
	}
	for _, c := range cases {
		got, _ := m.ToVMText(c.in)
		if got != c.want {
			t.Errorf("ToVMText(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

func TestToWinText(t *testing.T) {
	m := newMapper(t)
	cases := []struct{ in, want string }{
		{"Edited /home/fclaude/app/x.ts:12 and /home/fclaude/lib/", `Edited C:\dev\app\x.ts:12 and C:\dev\lib\`},
		{"[x](/home/fclaude/app/x.ts)", `[x](C:\dev\app\x.ts)`},
		{"Done: /home/fclaude/app/README.md.", `Done: C:\dev\app\README.md.`},
		{"see file:///home/fclaude/app/x", "see file:///c%3A/dev/app/x"},
		// untouched
		{"/srv/home/fclaude/app and ~/home/fclaude/x", "/srv/home/fclaude/app and ~/home/fclaude/x"},
		{"/home/fclaude/.claude/settings.json", "/home/fclaude/.claude/settings.json"},
		{"/home/fclaudex/a", "/home/fclaudex/a"},
		{"cwd /home/fclaude", "cwd /home/fclaude"},
	}
	for _, c := range cases {
		got, _ := m.ToWinText(c.in)
		if got != c.want {
			t.Errorf("ToWinText(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

func TestRootWithSpaces(t *testing.T) {
	m, err := New(`C:\Users\Zoë B\code`, "/home/fclaude")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.ToVMPath(`c:\users\zoë b\code\app`); got != "/home/fclaude/app" {
		t.Errorf("whole: %q", got)
	}
	if got, _ := m.ToWinPath("/home/fclaude/app/x"); got != `C:\Users\Zoë B\code\app\x` {
		t.Errorf("reverse: %q", got)
	}
	if got, _ := m.ToWinPath("file:///home/fclaude/app"); got != "file:///c%3A/Users/Zo%C3%AB%20B/code/app" {
		t.Errorf("uri: %q", got)
	}
}
