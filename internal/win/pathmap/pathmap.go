// Package pathmap translates file paths between the Windows host and the VM.
//
// A workspace C:\dev\app on the host is mounted in the VM at
// /home/fclaude/app. Claude runs in the VM and reports VM paths; the VS Code
// extension runs on Windows and speaks Windows paths. The fclaude-vscode
// wrapper sits on the stdio pipe between them and rewrites paths in both
// directions (see rewrite.go for the JSON stream side).
//
// Only paths strictly beneath the share root are mapped, and never a path
// whose first component starts with "." — on the VM side those are the VM
// account's own dot-directories (.claude, .ssh, ...), which do not exist on
// the host. Everything else passes through untouched.
package pathmap

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/robymus/fclaude/internal/win/winpath"
)

// Mapper converts paths between one Windows share root and one VM mount base.
type Mapper struct {
	winRoot string // cleaned, e.g. C:\dev
	vmRoot  string // e.g. /home/fclaude

	winText *regexp.Regexp // embedded Windows-path finder
	vmText  *regexp.Regexp // embedded VM-path finder
}

// Characters that end a path embedded in free text. Spaces are included (a
// heuristic: an embedded path containing a space is only partially mapped),
// as is ':' so "x.ts:12" line references keep their suffix.
const textTerminators = "\\s\"'`<>|*?\\x00()\\[\\]{},;:"

// New builds a Mapper. winRoot must be an absolute drive path that is not a
// drive root; vmRoot an absolute VM path.
func New(winRoot, vmRoot string) (*Mapper, error) {
	if !winpath.IsAbs(winRoot) || winpath.IsDriveRoot(winRoot) {
		return nil, fmt.Errorf("pathmap: windows root %q must be an absolute non-root drive path", winRoot)
	}
	vmRoot = path.Clean(vmRoot)
	if !strings.HasPrefix(vmRoot, "/") || vmRoot == "/" {
		return nil, fmt.Errorf("pathmap: vm root %q must be an absolute non-root path", vmRoot)
	}
	m := &Mapper{winRoot: winpath.Clean(winRoot), vmRoot: vmRoot}

	// Windows root as a regexp: drive (with ':' or URI-encoded %3A), then the
	// root's components joined by any run of separators, then an optional tail.
	segs := strings.Split(m.winRoot[3:], `\`)
	for i, s := range segs {
		segs[i] = regexp.QuoteMeta(s)
	}
	sep := `[\\/]+`
	m.winText = regexp.MustCompile(`(?i)(?:file:///)?` + regexp.QuoteMeta(m.winRoot[:1]) + `(?::|%3a)` + sep +
		strings.Join(segs, sep) + `(?:[\\/][^` + textTerminators + `]*)?`)
	m.vmText = regexp.MustCompile(`(?:file://)?` + regexp.QuoteMeta(m.vmRoot) + `(?:/[^` + textTerminators + `]*)?`)
	return m, nil
}

// WinRoot returns the cleaned Windows share root.
func (m *Mapper) WinRoot() string { return m.winRoot }

// VMRoot returns the VM mount base.
func (m *Mapper) VMRoot() string { return m.vmRoot }

// mappable reports whether a root-relative path may be translated.
func mappable(rel string) bool { return rel != "" && !strings.HasPrefix(rel, ".") }

// ToVMPath translates s if the whole string is a Windows path (or file URI)
// beneath the share root.
func (m *Mapper) ToVMPath(s string) (string, bool) {
	if p, ok := fileURIPath(s); ok {
		if !winpath.IsAbs(p) {
			return s, false
		}
		vm, ok := m.winToVM(p)
		if !ok {
			return s, false
		}
		return (&url.URL{Scheme: "file", Path: vm}).String(), true
	}
	if !winpath.IsAbs(s) {
		return s, false
	}
	return m.winToVM(s)
}

func (m *Mapper) winToVM(p string) (string, bool) {
	rel, ok := winpath.Rel(m.winRoot, p)
	if !ok || !mappable(rel) {
		return p, false
	}
	out := m.vmRoot + "/" + rel
	if strings.HasSuffix(p, `\`) || strings.HasSuffix(p, "/") {
		out += "/"
	}
	return out, true
}

// ToWinPath translates s if the whole string is a VM path (or file URI)
// beneath the VM mount base. File URIs are emitted in VS Code's canonical form
// (file:///c%3A/dev/...).
func (m *Mapper) ToWinPath(s string) (string, bool) {
	if p, ok := fileURIPath(s); ok {
		win, ok := m.vmToWin(p)
		if !ok {
			return s, false
		}
		return winFileURI(win), true
	}
	return m.vmToWin(s)
}

func (m *Mapper) vmToWin(p string) (string, bool) {
	if !strings.HasPrefix(p, m.vmRoot+"/") {
		return p, false
	}
	rel := strings.TrimPrefix(path.Clean(p), m.vmRoot+"/")
	if rel == path.Clean(p) || !mappable(rel) { // Clean escaped the root (e.g. "/home/fclaude/../x")
		return p, false
	}
	out := winpath.Join(m.winRoot, rel)
	if strings.HasSuffix(p, "/") {
		out += `\`
	}
	return out, true
}

// fileURIPath returns the decoded path of a file:// URI. For a Windows URI
// (file:///c%3A/dev) the leading slash before the drive is dropped.
func fileURIPath(s string) (string, bool) {
	if len(s) < 8 || !strings.EqualFold(s[:8], "file:///") {
		return "", false
	}
	p, err := url.PathUnescape(s[7:])
	if err != nil {
		return "", false
	}
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p, true
}

// winFileURI renders a Windows path as VS Code's Uri.toString() does:
// lower-case drive, ':' as %3A, forward slashes, segments percent-encoded.
func winFileURI(win string) string {
	segs := strings.Split(win[3:], `\`)
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "file:///" + strings.ToLower(win[:1]) + "%3A/" + strings.Join(segs, "/")
}

// ToVMText translates every Windows path beneath the share root embedded in
// free text.
func (m *Mapper) ToVMText(s string) (string, bool) {
	return replaceEmbedded(s, m.winText, isWinBoundaryBefore, m.ToVMPath)
}

// ToWinText translates every VM path beneath the mount base embedded in free text.
func (m *Mapper) ToWinText(s string) (string, bool) {
	return replaceEmbedded(s, m.vmText, isVMBoundaryBefore, m.ToWinPath)
}

func isWinBoundaryBefore(c byte) bool {
	return (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_'
}

func isVMBoundaryBefore(c byte) bool {
	return isWinBoundaryBefore(c) && c != '.' && c != '/' && c != '~' && c != '-'
}

// pathByte reports whether c can continue a path component; a root match
// followed by one is a different directory (C:\dev vs C:\devices).
func pathByte(c byte) bool {
	return !strings.ContainsRune(" \t\r\n\"'`<>|*?()[]{},;:\\/", rune(c)) && c != 0
}

func replaceEmbedded(s string, re *regexp.Regexp, boundaryBefore func(byte) bool, conv func(string) (string, bool)) (string, bool) {
	locs := re.FindAllStringIndex(s, -1)
	if locs == nil {
		return s, false
	}
	var b strings.Builder
	last, changed := 0, false
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if start > 0 && !boundaryBefore(s[start-1]) {
			continue
		}
		// Sentence punctuation directly after a path is not part of it.
		for end > start && (s[end-1] == '.') {
			end--
		}
		if end < len(s) && pathByte(s[end]) && s[end] != '.' {
			continue
		}
		out, ok := conv(s[start:end])
		if !ok {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(out)
		last, changed = end, true
	}
	if !changed {
		return s, false
	}
	b.WriteString(s[last:])
	return b.String(), true
}
