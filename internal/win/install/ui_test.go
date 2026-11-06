package install

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestConsoleUI(t *testing.T) {
	var out bytes.Buffer
	u := NewConsoleUI(strings.NewReader("\nD:\\code\nmaybe\ny\n\n"), &out)
	if v, err := u.Ask("Projects folder", `C:\dev`); err != nil || v != `C:\dev` {
		t.Errorf("default: %q %v", v, err)
	}
	if v, _ := u.Ask("Projects folder", `C:\dev`); v != `D:\code` {
		t.Errorf("answer: %q", v)
	}
	if ok, err := u.Confirm("Go?", false); err != nil || !ok { // "maybe" is re-asked, then "y"
		t.Errorf("confirm: %v %v", ok, err)
	}
	if ok, _ := u.Confirm("Go?", true); !ok {
		t.Error("empty answer must take the default")
	}
	if _, err := u.Confirm("Go?", true); !errors.Is(err, ErrNoInput) {
		t.Errorf("closed input: %v", err)
	}
	if _, err := u.Ask("x", "d"); !errors.Is(err, ErrNoInput) {
		t.Errorf("closed input: %v", err)
	}
	u.Step(3, 11, "Folders")
	u.Info("hello %d", 1)
	u.Warn("careful")
	for _, n := range []int64{10 << 20, 49 << 20, 50 << 20, 60 << 20, 120 << 20} {
		u.Progress("download", n, 0)
	}
	u.Pause()
	s := out.String()
	for _, want := range []string{"[3/11] Folders", "  hello 1", "  ! careful", "download: 50 MB", "download: 120 MB", "Press Enter"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "download:") != 2 {
		t.Errorf("progress not throttled:\n%s", s)
	}
}
