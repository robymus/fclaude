package pathmap

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Replays of the R4 Windows captures (testdata/windows/README.md).
//
// The VM side is synthesized: a native line rewritten ToVM stands in for what
// claude in the VM emits. The real VM output is checked by hand in R5.

// shareRootRe finds a share-root path in raw NDJSON (one or two levels of
// JSON escaping).
var shareRootRe = regexp.MustCompile(`(?i)c:(\\\\|\\|/)+dev`)

type capturedLine struct {
	Dir  string `json:"dir"`
	Line string `json:"line"`
}

func loadCapture(t *testing.T) []capturedLine {
	t.Helper()
	f, err := os.Open("testdata/windows/r4-vscode-2.1.318.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []capturedLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var c capturedLine
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("empty capture")
	}
	return out
}

// captureRewriter builds the rewriter the wrapper will: share root C:\dev,
// spelled as the extension's cwd spells it.
func captureRewriter(t *testing.T) *Rewriter {
	t.Helper()
	m, err := New(RootSpelling(`C:\dev`, `c:\dev\r4-scratch`), "/home/fclaude")
	if err != nil {
		t.Fatal(err)
	}
	return NewRewriter(m, DefaultPolicy())
}

// isPartialJSON reports a streaming tool-input delta. Its paths arrive split
// across chunks, so a line-local rewriter cannot map them; the complete input
// follows in the assistant message. Whether the extension shows paths from
// the deltas is an R5 check (PLAN §0.7).
func isPartialJSON(line string) bool {
	return strings.Contains(line, `"type":"input_json_delta"`)
}

func TestReplayExtensionToVM(t *testing.T) {
	rw := captureRewriter(t)
	n := 0
	for _, c := range loadCapture(t) {
		if c.Dir != "in" {
			continue
		}
		n++
		if got := string(rw.Line([]byte(c.Line), ToVM)); shareRootRe.MatchString(got) {
			t.Errorf("Windows path left after ToVM:\n%s", got)
		}
	}
	if n == 0 {
		t.Fatal("no extension → claude lines in capture")
	}
}

func TestReplayVMToExtension(t *testing.T) {
	rw := captureRewriter(t)
	exact, caseOnly, partial := 0, 0, 0
	for _, c := range loadCapture(t) {
		if c.Dir != "out" {
			continue
		}
		vm := string(rw.Line([]byte(c.Line), ToVM))
		if isPartialJSON(c.Line) {
			partial++
			continue
		}
		if shareRootRe.MatchString(vm) {
			t.Errorf("Windows path left in synthesized VM line:\n%s", vm)
			continue
		}
		back := string(rw.Line([]byte(vm), ToWin))
		switch {
		case back == c.Line:
			exact++
		case strings.EqualFold(back, c.Line) && driveOnly(back, c.Line):
			// Native claude itself mixes c:\ and C:\ (the model writes C:\);
			// the VM path cannot carry that, so only the drive letter differs.
			caseOnly++
		default:
			t.Errorf("round trip differs:\nnative: %s\nback:   %s", c.Line, back)
		}
	}
	if exact == 0 {
		t.Fatal("no exact round trips")
	}
	if partial != 1 {
		t.Errorf("expected the one known partial_json line, got %d", partial)
	}
	t.Logf("exact=%d drive-case-only=%d partial_json=%d", exact, caseOnly, partial)
}

// driveOnly reports whether a and b (equal under EqualFold) differ only in
// the drive letter of share-root paths.
func driveOnly(a, b string) bool {
	norm := func(s string) string {
		return shareRootRe.ReplaceAllStringFunc(s, strings.ToLower)
	}
	return norm(a) == norm(b)
}

func TestReplayArgs(t *testing.T) {
	b, err := os.ReadFile("testdata/windows/r4-argv.json")
	if err != nil {
		t.Fatal(err)
	}
	var sessions []struct {
		Cwd  string   `json:"cwd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(b, &sessions); err != nil {
		t.Fatal(err)
	}
	rw := captureRewriter(t)
	sawAddDir := false
	for _, s := range sessions {
		if got, ok := rw.m.ToVMPath(s.Cwd); !ok || got != "/home/fclaude/r4-scratch" {
			t.Errorf("cwd %q -> %q %v", s.Cwd, got, ok)
		}
		for i, a := range rw.Args(s.Args) {
			if shareRootRe.MatchString(a) {
				t.Errorf("arg %q not mapped (%q)", s.Args[i], a)
			}
			if strings.HasPrefix(s.Args[i], "--add-dir=") {
				sawAddDir = true
				if a != "--add-dir=/home/fclaude/r4-extra" {
					t.Errorf("add-dir -> %q", a)
				}
			}
		}
	}
	if !sawAddDir {
		t.Error("capture has no --add-dir (multi-root workspace)")
	}
}
