// Package mcphelper recognizes workspace-agnostic claude invocations.
//
// Port of fc_is_mcp_helper from host/bin/fclaude-vscode. The VS Code extension
// runs every claude process through the wrapper, including long-lived MCP
// helpers such as the claude-in-chrome server. Those don't touch workspace
// files, and the chrome MCP is a per-extension-host singleton that outlives
// the editing session — mounting for it would pin the first workspace's share
// until VS Code restarts. Such invocations run in the VM home with no mount.
//
// The discriminator is fail-open: anything unrecognized is treated as a real
// editing session and mounts as usual.
//
// Windows addition: `claude auth …` (the extension runs `auth status --json`
// for every new tab) is workspace-agnostic too, and skipping its mount saves
// a share round trip per tab.
package mcphelper

// Is reports whether args (claude's arguments, without the binary path) are
// an MCP-helper invocation.
func Is(args []string) bool {
	if len(args) > 0 && (args[0] == "mcp" || args[0] == "auth") { // `claude mcp serve|list|…`, `claude auth status`
		return true
	}
	for _, a := range args {
		if a == "--claude-in-chrome-mcp" {
			return true
		}
	}
	return false
}
