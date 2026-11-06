package mcphelper

import "testing"

// Cases mirror the Linux wrapper's behaviour (host/bin/fclaude-vscode).
func TestIs(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"mcp", "serve"}, true},
		{[]string{"mcp"}, true},
		{[]string{"--output-format", "stream-json", "--claude-in-chrome-mcp"}, true},
		{[]string{"--claude-in-chrome-mcp"}, true},
		{nil, false},
		{[]string{"--output-format", "stream-json", "--verbose"}, false},
		{[]string{"--mcp-config", "{}"}, false},       // flag mentioning mcp is not a helper
		{[]string{"-p", "mcp"}, false},                // "mcp" only counts as the subcommand
		{[]string{"--claude-in-chrome-mcp=x"}, false}, // exact match, like the bash version
	}
	for _, c := range cases {
		if got := Is(c.args); got != c.want {
			t.Errorf("Is(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
