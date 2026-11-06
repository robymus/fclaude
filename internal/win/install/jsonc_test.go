package install

import (
	"os"
	"strings"
	"testing"
)

const wrapperKey = "claudeCode.claudeProcessWrapper"
const wrapperVal = `C:\Users\bro\.fclaude\bin\fclaude-vscode.exe`
const wrapperJSON = `"C:\\Users\\bro\\.fclaude\\bin\\fclaude-vscode.exe"`

func TestSetJSONCString(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty file", "", "{\n    \"" + wrapperKey + "\": " + wrapperJSON + "\n}\n"},
		{"empty object", "{}\n", "{\n    \"" + wrapperKey + "\": " + wrapperJSON + "\n}\n"},
		{"append", "{\n  \"a\": 1\n}\n", "{\n  \"a\": 1,\n  \"" + wrapperKey + "\": " + wrapperJSON + "\n}\n"},
		{"trailing comma kept",
			"{\n\t\"a\": [1, 2],\n}\n",
			"{\n\t\"a\": [1, 2],\n\t\"" + wrapperKey + "\": " + wrapperJSON + ",\n}\n"},
		{"replace in place, comments untouched",
			"{\n    // the wrapper\n    \"" + wrapperKey + "\": \"old\", /* keep */\n    \"b\": true\n}\n",
			"{\n    // the wrapper\n    \"" + wrapperKey + "\": " + wrapperJSON + ", /* keep */\n    \"b\": true\n}\n"},
		{"replace non-string value", "{\"" + wrapperKey + "\": {\"x\": [1]}}", "{\"" + wrapperKey + "\": " + wrapperJSON + "}"},
		{"nested same key untouched",
			"{\n  \"outer\": {\"" + wrapperKey + "\": \"nested\"}\n}\n",
			"{\n  \"outer\": {\"" + wrapperKey + "\": \"nested\"},\n  \"" + wrapperKey + "\": " + wrapperJSON + "\n}\n"},
		{"tricky strings and comments",
			"{\n  \"s\": \"} // not a comment {\", // real comment with \"quote\n  \"t\": \"a\\\"b\"\n  /* last { */\n}",
			"{\n  \"s\": \"} // not a comment {\", // real comment with \"quote\n  \"t\": \"a\\\"b\",\n  \"" + wrapperKey + "\": " + wrapperJSON + "\n  /* last { */\n}"},
		{"CRLF", "{\r\n    \"a\": 1\r\n}\r\n", "{\r\n    \"a\": 1,\r\n    \"" + wrapperKey + "\": " + wrapperJSON + "\r\n}\r\n"},
	}
	for _, c := range cases {
		got, err := SetJSONCString([]byte(c.in), wrapperKey, wrapperVal)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		// Idempotent: setting it again changes nothing.
		again, err := SetJSONCString(got, wrapperKey, wrapperVal)
		if err != nil || string(again) != string(got) {
			t.Errorf("%s: not idempotent: %q", c.name, again)
		}
	}
}

func TestSetJSONCStringRealSettings(t *testing.T) {
	// The R4 machine's real settings.json (sanitized), incl. a trailing comma
	// inside a nested array.
	b, err := os.ReadFile("testdata/settings-real.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SetJSONCString(b, wrapperKey, wrapperVal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), string(b[:len(b)-3])) { // only the tail changes
		t.Errorf("edit touched existing content:\n%s", got)
	}
	if !strings.Contains(string(got), `"claudeCode.preferredLocation": "panel",`+"\r\n    \""+wrapperKey+`": `+wrapperJSON+"\r\n}") { // file is CRLF, no final newline
		t.Errorf("member not appended in house style:\n%s", got)
	}
}

func TestSetJSONCStringRejects(t *testing.T) {
	for _, in := range []string{"[1, 2]", "{\"a\": 1", "{\"a\": \"x}", "{ /* open", "{\"" + wrapperKey + "\": }"} {
		if _, err := SetJSONCString([]byte(in), wrapperKey, wrapperVal); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
