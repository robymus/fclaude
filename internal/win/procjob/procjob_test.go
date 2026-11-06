package procjob

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestChildDiesWithParent starts a helper "parent" process (this test binary
// in helper mode) which starts a long-sleeping child via Prepare/Attach and
// prints its PID; killing the parent must take the child down with it.
func TestChildDiesWithParent(t *testing.T) {
	if os.Getenv("PROCJOB_HELPER") == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestChildDiesWithParent$")
		child.Env = append(os.Environ(), "PROCJOB_HELPER=child")
		Prepare(child)
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := Attach(child); err != nil {
			os.Exit(4)
		}
		_, _ = os.Stdout.WriteString(strconv.Itoa(child.Process.Pid) + "\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if os.Getenv("PROCJOB_HELPER") == "child" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}

	parent := exec.Command(os.Args[0], "-test.run=^TestChildDiesWithParent$")
	parent.Env = append(os.Environ(), "PROCJOB_HELPER=parent")
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, err := out.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("no child pid from helper: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.Process.Kill() // TerminateProcess on Windows: no cleanup runs
	_ = parent.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child %d outlived its killed parent", pid)
}
