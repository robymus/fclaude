package pathmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func newRewriter(t *testing.T) *Rewriter {
	t.Helper()
	return NewRewriter(newMapper(t), DefaultPolicy())
}

func TestLineUnchangedIsIdentical(t *testing.T) {
	r := newRewriter(t)
	for _, in := range []string{
		`{"type":"system","n":1.50,"big":12345678901234567890,"x":[true,null]}` + "\n",
		`{ "spaced" : [ 1 , 2 ] , "s":"\u00e9\n<&>" }`,
		`not json at all {`,
		`{"path":"C:\\Windows\\x"}`,
		``,
	} {
		out := r.Line([]byte(in), ToVM)
		if string(out) != in {
			t.Errorf("unchanged line altered:\n in  %q\n out %q", in, out)
		}
	}
}

func TestLineToWinRewritesPathsOnly(t *testing.T) {
	r := newRewriter(t)
	in := `{"type":"assistant","message":{"content":[` +
		`{"type":"text","text":"Edited /home/fclaude/app/x.ts:3 <ok>"},` +
		`{"type":"tool_use","name":"Edit","input":{"file_path":"/home/fclaude/app/x.ts",` +
		`"old_string":"const p = \"/home/fclaude/app/a\"","new_string":"/home/fclaude/app/b",` +
		`"edits":[{"old_string":"/home/fclaude/app/c","new_string":"d"}]}}]},"cwd":"/home/fclaude/app","n":1.0}` + "\n"
	out := r.Line([]byte(in), ToWin)

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	content := got["message"].(map[string]any)["content"].([]any)
	if txt := content[0].(map[string]any)["text"]; txt != `Edited C:\dev\app\x.ts:3 <ok>` {
		t.Errorf("text: %q", txt)
	}
	input := content[1].(map[string]any)["input"].(map[string]any)
	if input["file_path"] != `C:\dev\app\x.ts` {
		t.Errorf("file_path: %q", input["file_path"])
	}
	if input["old_string"] != `const p = "/home/fclaude/app/a"` || input["new_string"] != "/home/fclaude/app/b" {
		t.Errorf("file content was rewritten: %q / %q", input["old_string"], input["new_string"])
	}
	if e := input["edits"].([]any)[0].(map[string]any); e["old_string"] != "/home/fclaude/app/c" {
		t.Errorf("nested edit content was rewritten: %q", e["old_string"])
	}
	if got["cwd"] != `C:\dev\app` {
		t.Errorf("cwd: %q", got["cwd"])
	}
	// Untouched bytes are preserved verbatim (number formatting, '<', key order, newline).
	for _, frag := range []string{`"n":1.0}`, `<ok>`, `{"type":"assistant","message"`} {
		if !bytes.Contains(out, []byte(frag)) {
			t.Errorf("fragment %q not preserved in %s", frag, out)
		}
	}
	if !bytes.HasSuffix(out, []byte("}\n")) {
		t.Error("trailing newline lost")
	}
}

func TestLineToVMPermissionResponse(t *testing.T) {
	r := newRewriter(t)
	in := `{"type":"control_response","response":{"behavior":"allow",` +
		`"updatedInput":{"file_path":"C:\\dev\\app\\x.ts","content":"path C:\\dev\\app stays"}}}`
	out := r.Line([]byte(in), ToVM)
	var got struct {
		Response struct {
			UpdatedInput map[string]string `json:"updatedInput"`
		} `json:"response"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Response.UpdatedInput["file_path"] != "/home/fclaude/app/x.ts" {
		t.Errorf("file_path: %q", got.Response.UpdatedInput["file_path"])
	}
	if got.Response.UpdatedInput["content"] != `path C:\dev\app stays` {
		t.Errorf("content rewritten: %q", got.Response.UpdatedInput["content"])
	}
}

func TestContentOutsideInputIsRewritten(t *testing.T) {
	r := newRewriter(t)
	in := `{"type":"user","message":{"role":"user","content":"look at C:\\dev\\app\\x.ts"}}`
	out := r.Line([]byte(in), ToVM)
	if !strings.Contains(string(out), `look at /home/fclaude/app/x.ts`) {
		t.Errorf("user prompt not rewritten: %s", out)
	}
}

func TestEmbeddedDisabled(t *testing.T) {
	p := DefaultPolicy()
	p.Embedded = false
	r := NewRewriter(newMapper(t), p)
	out := string(r.Line([]byte(`{"a":"/home/fclaude/x","b":"see /home/fclaude/x"}`), ToWin))
	if out != `{"a":"C:\\dev\\x","b":"see /home/fclaude/x"}` {
		t.Errorf("got %s", out)
	}
}

func TestArgs(t *testing.T) {
	r := newRewriter(t)
	in := []string{
		"--output-format", "stream-json",
		"--add-dir", `C:\dev\lib`,
		`--settings=C:\dev\app\s.json`,
		"--mcp-config", `{"mcpServers":{"x":{"command":"C:\\dev\\tools\\x.exe"}}}`,
		"--model=opus",
		`C:\Windows\x`,
	}
	want := []string{
		"--output-format", "stream-json",
		"--add-dir", "/home/fclaude/lib",
		"--settings=/home/fclaude/app/s.json",
		"--mcp-config", `{"mcpServers":{"x":{"command":"/home/fclaude/tools/x.exe"}}}`,
		"--model=opus",
		`C:\Windows\x`,
	}
	if got := r.Args(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Args:\n got  %q\n want %q", got, want)
	}
}

func TestPipe(t *testing.T) {
	r := newRewriter(t)
	long := strings.Repeat("x", 200*1024) // longer than the reader buffer
	src := `{"p":"/home/fclaude/a"}` + "\n" + `{"long":"` + long + `"}` + "\n" + `garbage` + "\n" + `{"p":"/home/fclaude/b"}`
	var dst bytes.Buffer
	if err := r.Pipe(&dst, strings.NewReader(src), ToWin); err != nil {
		t.Fatal(err)
	}
	want := `{"p":"C:\\dev\\a"}` + "\n" + `{"long":"` + long + `"}` + "\n" + `garbage` + "\n" + `{"p":"C:\\dev\\b"}`
	if dst.String() != want {
		t.Errorf("Pipe output mismatch (len got %d want %d)", dst.Len(), len(want))
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestPipeErrors(t *testing.T) {
	r := newRewriter(t)
	if err := r.Pipe(failWriter{}, strings.NewReader("{}\n"), ToVM); err == nil {
		t.Error("write error not propagated")
	}
	if err := r.Pipe(&bytes.Buffer{}, failReader{}, ToVM); err == nil {
		t.Error("read error not propagated")
	}
}

func FuzzLine(f *testing.F) {
	f.Add([]byte(`{"a":"/home/fclaude/x","input":{"content":"/home/fclaude/y"}}`))
	f.Add([]byte(`{"a":"C:\\dev\\x y","b":[1,"c:/dev/z",{"k":"\u0041"}]}`))
	r := NewRewriter(func() *Mapper { m, _ := New(`C:\dev`, "/home/fclaude"); return m }(), DefaultPolicy())
	f.Fuzz(func(t *testing.T, in []byte) {
		for _, dir := range []Direction{ToVM, ToWin} {
			out := r.Line(in, dir)
			if json.Valid(in) != json.Valid(out) {
				t.Fatalf("validity changed: %q -> %q", in, out)
			}
		}
	})
}
