package capture

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The test binary doubles as the "claude" being captured.
func TestMain(m *testing.M) {
	if os.Getenv("CAPTURE_HELPER") == "echo" {
		// Echo stdin to stdout upper-cased, say something on stderr, exit 7.
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			_, _ = os.Stdout.WriteString(strings.ToUpper(sc.Text()) + "\n")
		}
		_, _ = os.Stderr.WriteString("bye\n")
		os.Exit(7)
	}
	os.Exit(m.Run())
}

type rec struct {
	K    string         `json:"k"`
	D    *string        `json:"d"`
	B64  string         `json:"b64"`
	Code *int           `json:"code"`
	Meta map[string]any `json:"meta"`
}

func parseLog(t *testing.T, b []byte) []rec {
	t.Helper()
	var out []rec
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var r rec
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func stream(recs []rec, kind string) string {
	var b strings.Builder
	for _, r := range recs {
		if r.K == kind && r.D != nil {
			b.WriteString(*r.D)
		}
	}
	return b.String()
}

func TestRunRecordsEverything(t *testing.T) {
	t.Setenv("CAPTURE_HELPER", "echo")
	t.Setenv("MY_API_KEY", "sk-should-not-leak")
	var stdout, stderr, log bytes.Buffer
	code, err := Run(Options{
		Command: os.Args[0],
		Args:    []string{"-test.run=^$", "--flag", `C:\dev\x`},
		Wrapper: []string{"fclaude-capture.exe", "claude.exe"},
		Stdin:   strings.NewReader("{\"a\":1}\nhello\n"),
		Stdout:  &stdout, Stderr: &stderr, Log: &log,
	})
	if err != nil || code != 7 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if stdout.String() != "{\"A\":1}\nHELLO\n" || stderr.String() != "bye\n" {
		t.Fatalf("passthrough broken: out=%q err=%q", stdout.String(), stderr.String())
	}
	recs := parseLog(t, log.Bytes())
	if recs[0].K != "meta" || recs[0].Meta["command"] != os.Args[0] {
		t.Fatalf("first record not meta: %+v", recs[0])
	}
	if !strings.Contains(log.String(), `C:\\dev\\x`) {
		t.Error("args not recorded")
	}
	if strings.Contains(log.String(), "sk-should-not-leak") || !strings.Contains(log.String(), "MY_API_KEY=<redacted>") {
		t.Error("secret env value not redacted")
	}
	if stream(recs, "in") != "{\"a\":1}\nhello\n" || stream(recs, "out") != stdout.String() || stream(recs, "err") != "bye\n" {
		t.Errorf("streams not reconstructible: in=%q out=%q err=%q", stream(recs, "in"), stream(recs, "out"), stream(recs, "err"))
	}
	last := recs[len(recs)-1]
	if last.K != "exit" || last.Code == nil || *last.Code != 7 {
		t.Errorf("last record: %+v", last)
	}
}

func TestRunMissingCommand(t *testing.T) {
	var log bytes.Buffer
	_, err := Run(Options{Command: "/nonexistent/claude", Stdin: strings.NewReader(""),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Log: &log})
	if err == nil {
		t.Fatal("missing command not reported")
	}
}

func TestChunkBinaryIsBase64(t *testing.T) {
	var log bytes.Buffer
	NewRecorder(&log).Chunk("out", []byte{0xff, 0xfe})
	recs := parseLog(t, log.Bytes())
	got, _ := base64.StdEncoding.DecodeString(recs[0].B64)
	if recs[0].D != nil || !bytes.Equal(got, []byte{0xff, 0xfe}) {
		t.Fatalf("binary chunk: %+v", recs[0])
	}
}

type failWriter struct{ n int }

func (f *failWriter) Write(_ []byte) (int, error) {
	f.n++
	return 0, errors.New("disk full")
}

func TestBrokenLogDoesNotBreakSession(t *testing.T) {
	fw := &failWriter{}
	r := NewRecorder(fw)
	r.Chunk("in", []byte("a"))
	r.Chunk("in", []byte("b"))
	if r.Err() == nil || fw.n != 1 {
		t.Fatalf("err=%v writes=%d (should stop after first failure)", r.Err(), fw.n)
	}
}

func TestRedactEnv(t *testing.T) {
	got := RedactEnv([]string{"PATH=/bin", "GITHUB_TOKEN=x", "Password=y", "NOEQUALS"})
	want := []string{"GITHUB_TOKEN=<redacted>", "NOEQUALS", "PATH=/bin", "Password=<redacted>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %q", got)
	}
}
