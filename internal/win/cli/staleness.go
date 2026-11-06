package cli

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// stalenessProbe runs in the VM (as the fclaude user) and prints the facts the
// staleness warning needs. Port of fclaude_staleness_warn in lib/common.sh:
// the apt-success stamp's CONTENT is the ISO time of the last real upgrade
// (its mtime would reset on a disk restore); the newest Claude version
// directory's mtime is its install time.
const stalenessProbe = `s=$(date -d "$(cat /var/lib/fclaude/last-apt-success 2>/dev/null)" +%s 2>/dev/null || echo -)
a=$(head -1 /var/lib/fclaude/last-apt-upgrade 2>/dev/null || true)
v=$(ls -1dt "$HOME/.local/share/claude/versions/"*/ 2>/dev/null | head -1)
if [ -n "$v" ]; then m=$(date -r "$v" +%s 2>/dev/null || echo -); else m=-; fi
printf "APT_OK=%s\nATTEMPT=%s\nCLAUDE=%s\n" "${s:--}" "${a:--}" "${m:--}"
`

// probeFacts is the parsed output of stalenessProbe. Epochs are -1 when unknown.
type probeFacts struct {
	AptOK   int64
	Attempt string // first word of the last attempt line: ok, skipped, error...
	Claude  int64
}

func parseProbe(out string) probeFacts {
	f := probeFacts{AptOK: -1, Claude: -1}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "APT_OK":
			f.AptOK = epoch(v)
		case "ATTEMPT":
			if w := strings.Fields(v); len(w) > 0 && v != "-" {
				f.Attempt = w[0]
			}
		case "CLAUDE":
			f.Claude = epoch(v)
		}
	}
	return f
}

func epoch(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// stalenessMessages is _fc_staleness_messages: zero or more warnings when the
// last successful update is older than days.
func stalenessMessages(now int64, days int, aptEnabled bool, f probeFacts) []string {
	if days <= 0 {
		return nil
	}
	maxAge := int64(days) * 86400
	var out []string
	if aptEnabled && (f.AptOK < 0 || now-f.AptOK > maxAge) {
		reason := ""
		switch {
		case strings.HasPrefix(f.Attempt, "error"):
			reason = " (last attempt FAILED - check: journalctl -u fclaude-apt-upgrade)"
		case strings.HasPrefix(f.Attempt, "skipped"):
			reason = " (recent runs skipped - slow or offline network)"
		case strings.HasPrefix(f.Attempt, "ok"):
		default:
			reason = " (no successful upgrade recorded yet)"
		}
		out = append(out, fmt.Sprintf("system packages not upgraded in %s%s; run: fclaude --update", daysAgo(f.AptOK, now), reason))
	}
	if f.Claude >= 0 && now-f.Claude > maxAge {
		out = append(out, fmt.Sprintf("Claude Code has not updated in %s - auto-updater may be stuck; run: fclaude --update", daysAgo(f.Claude, now)))
	}
	return out
}

func daysAgo(t, now int64) string {
	if t < 0 {
		return "an unknown time"
	}
	return fmt.Sprintf("%d days", (now-t)/86400)
}
