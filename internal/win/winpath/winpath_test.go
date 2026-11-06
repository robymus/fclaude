package winpath

import (
	"errors"
	"testing"
)

func TestIsAbs(t *testing.T) {
	cases := map[string]bool{
		`C:\dev`:        true,
		`c:/dev`:        true,
		`C:\`:           true,
		`\\?\C:\dev`:    true,
		`C:dev`:         false,
		`\dev`:          false,
		`\\server\shr`:  false,
		`/home/fclaude`: false,
		``:              false,
		`1:\x`:          false,
	}
	for in, want := range cases {
		if got := IsAbs(in); got != want {
			t.Errorf("IsAbs(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		`c:/dev/`:              `C:\dev`,
		`C:\\dev\\\app`:        `C:\dev\app`,
		`C:\dev\.\a\..\b`:      `C:\dev\b`,
		`C:\..\..`:             `C:\`,
		`c:\`:                  `C:\`,
		`\\?\c:\Dev\App`:       `C:\Dev\App`,
		`relative\path`:        `relative\path`,
		`C:\Users\Zoë\my app\`: `C:\Users\Zoë\my app`,
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDriveRoot(t *testing.T) {
	for in, want := range map[string]bool{`C:\`: true, `d:/`: true, `C:\dev`: false, `C:\dev\..`: true} {
		if got := IsDriveRoot(in); got != want {
			t.Errorf("IsDriveRoot(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRel(t *testing.T) {
	type tc struct {
		root, target, rel string
		ok                bool
	}
	cases := []tc{
		{`C:\dev`, `C:\dev`, "", true},
		{`C:\dev`, `c:\DEV\App\src`, "App/src", true},
		{`C:\dev\`, `C:/dev/app`, "app", true},
		{`C:\dev`, `C:\devices\x`, "", false}, // prefix of a component, not a parent
		{`C:\dev`, `D:\dev\x`, "", false},
		{`C:\dev`, `C:\`, "", false},
		{`C:\dev`, `relative`, "", false},
		{`C:\`, `C:\x\y`, "x/y", true},
	}
	for _, c := range cases {
		rel, ok := Rel(c.root, c.target)
		if rel != c.rel || ok != c.ok {
			t.Errorf("Rel(%q, %q) = (%q, %v), want (%q, %v)", c.root, c.target, rel, ok, c.rel, c.ok)
		}
	}
}

func TestWorkspaceRel(t *testing.T) {
	if rel, err := WorkspaceRel(`C:\dev`, `C:\dev\proj\sub`); err != nil || rel != "proj/sub" {
		t.Fatalf("got (%q, %v)", rel, err)
	}
	if _, err := WorkspaceRel(`C:\dev`, `C:\outside`); !errors.Is(err, ErrNotUnderRoot) {
		t.Fatalf("outside root: got %v", err)
	}
	if _, err := WorkspaceRel(`C:\dev`, `C:\dev`); err == nil {
		t.Fatal("root itself must be rejected")
	}
	if _, err := WorkspaceRel(`C:\dev`, `C:\dev\.claude`); err == nil {
		t.Fatal("dot top-level folder must be rejected")
	}
	if rel, err := WorkspaceRel(`C:\dev`, `C:\dev\proj\.git`); err != nil || rel != "proj/.git" {
		t.Fatalf("nested dot folder is fine: got (%q, %v)", rel, err)
	}
}

func TestJoin(t *testing.T) {
	if got := Join(`C:\dev`, "a/b"); got != `C:\dev\a\b` {
		t.Fatalf("Join = %q", got)
	}
	if got := Join(`c:/dev/`, ""); got != `C:\dev` {
		t.Fatalf("Join empty = %q", got)
	}
}
