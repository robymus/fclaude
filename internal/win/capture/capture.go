// Package capture records a claude process's full stdio conversation.
//
// It is the research tool for plan spike R4: the VS Code extension is pointed
// at fclaude-capture.exe as its process wrapper, which runs the extension's
// own bundled claude.exe natively (no VM) and logs argv, environment, cwd and
// both stdio directions. The result is ground truth for the path forms a
// native Windows claude sends and receives — the target the VM path rewriter
// must reproduce.
//
// The log is JSON Lines; each record is one of
//
//	{"t":0,"k":"meta","meta":{...}}
//	{"t":12,"k":"in","d":"..."}     extension → claude (stdin chunk)
//	{"t":15,"k":"out","d":"..."}    claude → extension (stdout chunk)
//	{"t":15,"k":"err","d":"..."}    claude stderr chunk
//	{"t":99,"k":"exit","code":0}
//
// t is milliseconds since start. Chunks are raw reads, not lines; concatenate
// a direction's "d" values to recover its exact byte stream. Chunks that are
// not valid UTF-8 are stored base64-encoded in "b64" instead of "d".
//
// The log contains whatever was said in the session (prompts, code, tool
// output). Environment values whose names look secret are redacted, but the
// stream itself is not: sanitize before committing fixtures.
package capture

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/robymus/fclaude/internal/win/procjob"
)

// Recorder writes capture records; safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	enc   *json.Encoder
	start time.Time
	err   error
}

// NewRecorder returns a Recorder writing JSON Lines to w.
func NewRecorder(w io.Writer) *Recorder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Recorder{enc: enc, start: time.Now()}
}

type record struct {
	T    int64          `json:"t"`
	K    string         `json:"k"`
	D    *string        `json:"d,omitempty"`
	B64  string         `json:"b64,omitempty"`
	Code *int           `json:"code,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
}

func (r *Recorder) write(rec record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return // a broken log must never break the session it observes
	}
	rec.T = time.Since(r.start).Milliseconds()
	r.err = r.enc.Encode(rec)
}

// Chunk records raw bytes for stream kind ("in", "out", "err").
func (r *Recorder) Chunk(kind string, b []byte) {
	rec := record{K: kind}
	if utf8.Valid(b) {
		s := string(b)
		rec.D = &s
	} else {
		rec.B64 = base64.StdEncoding.EncodeToString(b)
	}
	r.write(rec)
}

// Err returns the first log write error, if any.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// secretName matches environment variable names whose values are redacted.
var secretName = regexp.MustCompile(`(?i)(key|token|secret|pass|auth|cookie|credential|session)`)

// RedactEnv returns env (KEY=VALUE entries) sorted, with secret-looking values replaced.
func RedactEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && secretName.MatchString(k) {
			kv = k + "=<redacted>"
		}
		out = append(out, kv)
	}
	sort.Strings(out)
	return out
}

// teeWriter forwards to w and records every chunk.
type teeWriter struct {
	w    io.Writer
	rec  *Recorder
	kind string
}

func (t teeWriter) Write(p []byte) (int, error) {
	t.rec.Chunk(t.kind, p)
	return t.w.Write(p)
}

// Options configure Run.
type Options struct {
	Command string   // program to run (the extension's bundled claude)
	Args    []string // its arguments
	Wrapper []string // the wrapper's own argv, for the meta record
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Log     io.Writer
}

// Run executes the command with all stdio recorded and returns its exit code.
// The child is killed if this process dies (see procjob).
func Run(o Options) (int, error) {
	rec := NewRecorder(o.Log)
	cwd, _ := os.Getwd()
	rec.write(record{K: "meta", Meta: map[string]any{
		"wrapper_argv": o.Wrapper,
		"command":      o.Command,
		"args":         o.Args,
		"cwd":          cwd,
		"env":          RedactEnv(os.Environ()),
		"goos":         runtime.GOOS,
		"time":         time.Now().UTC().Format(time.RFC3339),
	}})

	cmd := exec.Command(o.Command, o.Args...) //nolint:gosec // capture tool: runs the program it was told to
	cmd.Stdout = teeWriter{o.Stdout, rec, "out"}
	cmd.Stderr = teeWriter{o.Stderr, rec, "err"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 1, err
	}
	procjob.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	if err := procjob.Attach(cmd); err != nil {
		rec.write(record{K: "meta", Meta: map[string]any{"warning": err.Error()}})
	}
	go func() {
		// Forward stdin until EOF, then close the child's stdin so it sees EOF
		// too. If the child exits first this goroutine is abandoned with the process.
		_, _ = io.Copy(teeWriter{stdin, rec, "in"}, o.Stdin)
		_ = stdin.Close()
	}()

	code := 0
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return 1, err
		}
		code = ee.ExitCode()
	}
	rec.write(record{K: "exit", Code: &code})
	return code, rec.Err()
}
