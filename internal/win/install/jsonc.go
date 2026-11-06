package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SetJSONCString sets a top-level string member in a JSONC document (VS Code's
// settings.json: comments and trailing commas allowed) and returns the edited
// document. Everything else — comments, formatting, member order, trailing
// commas, line endings — is preserved byte-for-byte: an existing member has
// only its value replaced; a new member is inserted after the last one,
// matching the file's indentation and newline style.
func SetJSONCString(src []byte, key, value string) ([]byte, error) {
	val, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	k, _ := json.Marshal(key)
	if len(bytes.TrimSpace(src)) == 0 {
		return []byte(fmt.Sprintf("{\n    %s: %s\n}\n", k, val)), nil
	}
	toks, err := lexJSONC(src)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 || toks[0].kind != '{' {
		return nil, errors.New("settings.json: the top level is not a JSON object")
	}
	// Walk the top-level members: '{' (key ':' value ','?)* '}'.
	depth, lastEnd, closeAt := 0, -1, -1
	lastIsComma := false
	indent := ""
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.kind {
		case '{', '[':
			depth++
			continue // a member ends at its closing bracket, never at an opening one
		case '}', ']':
			depth--
			if depth == 0 {
				closeAt = t.start
				i = len(toks)
				continue
			}
			if depth == 1 {
				lastEnd, lastIsComma = t.end, false
			}
			continue
		}
		if depth != 1 {
			continue
		}
		switch t.kind {
		case ',':
			lastIsComma = true
		case 's':
			// A string at depth 1 followed by ':' is a member key.
			if i+1 < len(toks) && toks[i+1].kind == ':' {
				if indent == "" {
					indent = lineIndent(src, t.start)
				}
				name := t.text(src)
				if name == key {
					vs, ve, ok := valueSpan(toks, i+2)
					if !ok {
						return nil, fmt.Errorf("settings.json: no value for %q", key)
					}
					return splice(src, vs, ve, val), nil
				}
				i++ // skip ':'
				continue
			}
			lastEnd, lastIsComma = t.end, false
		default:
			lastEnd, lastIsComma = t.end, false
		}
	}
	if closeAt < 0 {
		return nil, errors.New("settings.json: unterminated top-level object")
	}
	nl := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		nl = "\r\n"
	}
	if indent == "" {
		indent = "    "
	}
	member := fmt.Sprintf("%s%s: %s", indent, k, val)
	switch {
	case lastEnd < 0: // empty object
		return splice(src, toks[0].end, closeAt, []byte(nl+member+nl)), nil
	case lastIsComma: // keep the trailing-comma style
		comma := lastCommaAfter(toks, lastEnd)
		return splice(src, comma, comma, []byte(nl+member+",")), nil
	default:
		return splice(src, lastEnd, lastEnd, []byte(","+nl+member)), nil
	}
}

type token struct {
	kind       byte // '{' '}' '[' ']' ':' ',' 's' (string) 'v' (other scalar)
	start, end int
}

func (t token) text(src []byte) string {
	var s string
	_ = json.Unmarshal(src[t.start:t.end], &s)
	return s
}

// lexJSONC tokenizes, skipping whitespace and // and /* */ comments.
func lexJSONC(src []byte) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == 0xEF || c == 0xBB || c == 0xBF: // ws, UTF-8 BOM
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return nil, errors.New("settings.json: unterminated /* comment")
			}
			i += end + 4
		case strings.IndexByte("{}[]:,", c) >= 0:
			toks = append(toks, token{c, i, i + 1})
			i++
		case c == '"':
			j := i + 1
			for ; j < len(src); j++ {
				if src[j] == '\\' {
					j++
					continue
				}
				if src[j] == '"' {
					break
				}
			}
			if j >= len(src) {
				return nil, errors.New("settings.json: unterminated string")
			}
			toks = append(toks, token{'s', i, j + 1})
			i = j + 1
		default:
			j := i
			for j < len(src) && strings.IndexByte(" \t\r\n{}[]:,/", src[j]) < 0 {
				j++
			}
			toks = append(toks, token{'v', i, j})
			i = j
		}
	}
	return toks, nil
}

// valueSpan returns the byte span of the value starting at toks[i].
func valueSpan(toks []token, i int) (start, end int, ok bool) {
	if i >= len(toks) {
		return 0, 0, false
	}
	t := toks[i]
	if t.kind != '{' && t.kind != '[' {
		return t.start, t.end, t.kind == 's' || t.kind == 'v'
	}
	depth := 0
	for j := i; j < len(toks); j++ {
		switch toks[j].kind {
		case '{', '[':
			depth++
		case '}', ']':
			if depth--; depth == 0 {
				return t.start, toks[j].end, true
			}
		}
	}
	return 0, 0, false
}

func lastCommaAfter(toks []token, pos int) int {
	for _, t := range toks {
		if t.kind == ',' && t.start >= pos {
			return t.end
		}
	}
	return pos
}

func lineIndent(src []byte, pos int) string {
	start := bytes.LastIndexByte(src[:pos], '\n') + 1
	ind := src[start:pos]
	if len(bytes.TrimLeft(ind, " \t")) != 0 { // member does not start its line
		return ""
	}
	return string(ind)
}

func splice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	return append(out, src[end:]...)
}

// DeleteJSONCMember removes a top-level member from a JSONC document,
// preserving everything else byte-for-byte. A member that sits alone on its
// line(s) takes its line with it. Absent key: src is returned unchanged.
func DeleteJSONCMember(src []byte, key string) ([]byte, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return src, nil
	}
	toks, err := lexJSONC(src)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 || toks[0].kind != '{' {
		return nil, errors.New("settings.json: the top level is not a JSON object")
	}
	depth, prevComma := 0, -1 // prevComma: token index of the last depth-1 comma
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.kind {
		case '{', '[':
			depth++
			continue
		case '}', ']':
			depth--
			continue
		}
		if depth != 1 {
			continue
		}
		if t.kind == ',' {
			prevComma = i
			continue
		}
		if t.kind != 's' || i+1 >= len(toks) || toks[i+1].kind != ':' {
			continue
		}
		_, ve, ok := valueSpan(toks, i+2)
		if !ok {
			return nil, fmt.Errorf("settings.json: no value for %q", t.text(src))
		}
		j := i + 2 // first token after the value
		for j < len(toks) && toks[j].start < ve {
			j++
		}
		if t.text(src) != key {
			i = j - 1 // skip the whole value (nested brackets included)
			continue
		}
		start, end := t.start, ve
		switch {
		case j < len(toks) && toks[j].kind == ',':
			end = toks[j].end // take our trailing comma
		case prevComma >= 0:
			start = toks[prevComma].start // last member: take the comma before it
		}
		// Whole lines: extend over leading indentation and the line break.
		ls := bytes.LastIndexByte(src[:start], '\n') + 1
		if len(bytes.TrimLeft(src[ls:start], " \t")) == 0 {
			if nl := bytes.IndexByte(src[end:], '\n'); nl >= 0 && len(bytes.TrimSpace(src[end:end+nl])) == 0 {
				start, end = ls, end+nl+1
			}
		}
		return splice(src, start, end, nil), nil
	}
	return src, nil
}
