R4 captures (PLAN §6): the VS Code Claude Code extension 2.1.318 on Windows 11,
running its bundled `claude.exe` natively through `fclaude-capture.exe`
(2026-11-06). Workspace `c:\dev\r4-scratch`, second workspace folder
`c:\dev\r4-extra`.

- `r4-vscode-2.1.318.jsonl`: every stdio JSON line that carries a share-root
  path, as `{"dir": "in"|"out", "line": "<raw NDJSON line>"}`. `in` is
  extension → claude, `out` is claude → extension.
- `r4-argv.json`: argv and cwd of the stream-json sessions.

Sanitized: user name → `bro`. Environment, stderr (debug log) and lines
without a share-root path are not kept.
