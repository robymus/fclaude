package pathmap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Direction selects which way a Rewriter translates.
type Direction int

const (
	// ToVM rewrites Windows paths to VM paths (extension → claude).
	ToVM Direction = iota
	// ToWin rewrites VM paths to Windows paths (claude → extension).
	ToWin
)

// Policy decides which JSON string values may be rewritten.
//
// The stream carries tool inputs whose fields are literal file content (the
// new_string of an Edit, the content of a Write). Those are applied in the VM
// verbatim, so rewriting them — in either direction — would corrupt files or
// make a diff lie about what is written. Content fields are therefore
// protected whenever they appear anywhere inside a tool-input object.
type Policy struct {
	// InputKeys name objects holding tool input ("input", "updatedInput", ...).
	InputKeys map[string]bool
	// ContentKeys are file-content fields protected inside an input object.
	ContentKeys map[string]bool
	// Embedded enables translating paths embedded in free text, in addition to
	// strings that are entirely a path.
	Embedded bool
}

// DefaultPolicy is the initial policy, to be validated against captured
// Windows sessions (plan §6, R4/R5).
func DefaultPolicy() Policy {
	return Policy{
		InputKeys: set("input", "updatedInput", "tool_input", "toolInput"),
		ContentKeys: set("old_string", "new_string", "content", "new_source", "old_str", "new_str",
			"file_text", "text"),
		Embedded: true,
	}
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// Rewriter applies a Mapper to a JSON stream under a Policy.
type Rewriter struct {
	m *Mapper
	p Policy
}

// NewRewriter returns a Rewriter for m and p.
func NewRewriter(m *Mapper, p Policy) *Rewriter { return &Rewriter{m: m, p: p} }

// String rewrites one string value outside any JSON context.
func (r *Rewriter) String(s string, dir Direction) (string, bool) {
	whole, text := r.m.ToVMPath, r.m.ToVMText
	if dir == ToWin {
		whole, text = r.m.ToWinPath, r.m.ToWinText
	}
	if out, ok := whole(s); ok {
		return out, true
	}
	if r.p.Embedded {
		return text(s)
	}
	return s, false
}

// Line rewrites one NDJSON line. Bytes are preserved exactly except for the
// rewritten string literals; a line that is not valid JSON, or has nothing to
// rewrite, is returned unchanged (the same slice).
func (r *Rewriter) Line(line []byte, dir Direction) []byte {
	if !json.Valid(line) {
		return line
	}
	s := scanner{buf: line, rw: r, dir: dir}
	s.value(ctx{})
	if len(s.edits) == 0 {
		return line
	}
	var out bytes.Buffer
	out.Grow(len(line) + 64)
	last := 0
	for _, e := range s.edits {
		out.Write(line[last:e.start])
		out.Write(e.repl)
		last = e.end
	}
	out.Write(line[last:])
	return out.Bytes()
}

// Args rewrites command-line arguments toward the VM: whole-path args,
// --flag=<path> values, and inline JSON args (e.g. --mcp-config '{...}').
func (r *Rewriter) Args(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = r.arg(a)
	}
	return out
}

func (r *Rewriter) arg(a string) string {
	if strings.HasPrefix(a, "{") || strings.HasPrefix(a, "[") {
		return string(r.Line([]byte(a), ToVM))
	}
	if out, ok := r.m.ToVMPath(a); ok {
		return out
	}
	if strings.HasPrefix(a, "-") {
		if k, v, ok := strings.Cut(a, "="); ok {
			return k + "=" + r.arg(v)
		}
	}
	return a
}

// Pipe copies src to dst line by line, rewriting each line. Every line is
// written as soon as it is complete (the protocol is interactive), lines of
// any length are supported, and a final unterminated line is still processed.
func (r *Rewriter) Pipe(dst io.Writer, src io.Reader, dir Direction) error {
	br := bufio.NewReaderSize(src, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := dst.Write(r.Line(line, dir)); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// --- byte-preserving JSON scanner ------------------------------------------

type ctx struct {
	inInput   bool
	protected bool
}

func (c ctx) child(key string, p Policy) ctx {
	return ctx{
		inInput:   c.inInput || p.InputKeys[key],
		protected: c.protected || (c.inInput && p.ContentKeys[key]),
	}
}

type edit struct {
	start, end int
	repl       []byte
}

// scanner walks JSON that json.Valid has already accepted, so it can be
// lenient about errors it cannot encounter.
type scanner struct {
	buf   []byte
	i     int
	rw    *Rewriter
	dir   Direction
	edits []edit
}

func (s *scanner) ws() {
	for s.i < len(s.buf) {
		switch s.buf[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

func (s *scanner) value(c ctx) {
	s.ws()
	if s.i >= len(s.buf) {
		return
	}
	switch s.buf[s.i] {
	case '{':
		s.i++
		for {
			s.ws()
			if s.buf[s.i] == '}' {
				s.i++
				return
			}
			key := s.decodeString()
			s.ws()
			s.i++ // ':'
			s.value(c.child(key, s.rw.p))
			s.ws()
			if s.buf[s.i] == ',' {
				s.i++
				continue
			}
			s.i++ // '}'
			return
		}
	case '[':
		s.i++
		for {
			s.ws()
			if s.buf[s.i] == ']' {
				s.i++
				return
			}
			s.value(c)
			s.ws()
			if s.buf[s.i] == ',' {
				s.i++
				continue
			}
			s.i++ // ']'
			return
		}
	case '"':
		start := s.i
		str := s.decodeString()
		if c.protected {
			return
		}
		if out, ok := s.rw.String(str, s.dir); ok {
			s.edits = append(s.edits, edit{start: start, end: s.i, repl: encodeString(out)})
		}
	default: // number, true, false, null
		for s.i < len(s.buf) && !strings.ContainsRune(",]} \t\r\n", rune(s.buf[s.i])) {
			s.i++
		}
	}
}

// decodeString consumes the string literal at s.i and returns its value.
func (s *scanner) decodeString() string {
	start := s.i
	s.i++ // opening quote
	escaped := false
	for ; s.i < len(s.buf); s.i++ {
		c := s.buf[s.i]
		if c == '\\' {
			escaped = true
			s.i++ // skip the escaped byte
			continue
		}
		if c == '"' {
			break
		}
	}
	s.i++ // closing quote
	raw := s.buf[start:s.i]
	if !escaped {
		return string(raw[1 : len(raw)-1])
	}
	var out string
	_ = json.Unmarshal(raw, &out) // valid by construction
	return out
}

// encodeString renders v as a JSON string literal without HTML escaping, to
// match what the original producer would emit.
func encodeString(v string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // strings always encode
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
