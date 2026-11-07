// Package winpath implements the small subset of Windows path semantics the
// host tooling needs, as pure string functions.
//
// It deliberately does not use path/filepath: the logic must behave the same
// when unit-tested on Linux CI as when it runs on Windows, and only
// drive-letter paths (C:\...) are supported — UNC shares and device paths are
// rejected rather than half-handled.
package winpath

import (
	"errors"
	"fmt"
	"strings"
)

// isSep reports whether c is a Windows path separator.
func isSep(c byte) bool { return c == '\\' || c == '/' }

func isDriveLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// stripExtended removes a \\?\ extended-length prefix in front of a drive path.
func stripExtended(p string) string {
	if len(p) >= 6 && (strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `//?/`)) && isDriveLetter(p[4]) && p[5] == ':' {
		return p[4:]
	}
	return p
}

// IsAbs reports whether p is an absolute drive-letter path (C:\ or C:/ form,
// optionally with a \\?\ prefix).
func IsAbs(p string) bool {
	p = stripExtended(p)
	return len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && isSep(p[2])
}

// IsDriveRoot reports whether p is just a drive root such as C:\.
func IsDriveRoot(p string) bool {
	return IsAbs(p) && Clean(p) == strings.ToUpper(stripExtended(p)[:1])+`:\`
}

// Clean normalizes an absolute drive path: backslash separators, upper-case
// drive letter, no repeated separators, "." and ".." resolved, no trailing
// separator (except on a drive root). Non-absolute input is returned unchanged.
func Clean(p string) string {
	if !IsAbs(p) {
		return p
	}
	p = stripExtended(p)
	drive := strings.ToUpper(p[:1]) + ":"
	var parts []string
	for _, seg := range strings.FieldsFunc(p[2:], func(r rune) bool { return r == '\\' || r == '/' }) {
		switch seg {
		case ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, seg)
		}
	}
	return drive + `\` + strings.Join(parts, `\`)
}

// Rel returns the path of target relative to root, using forward slashes, if
// target is root itself ("" result) or lies beneath it. Comparison is
// case-insensitive, as on NTFS. Both inputs must be absolute.
func Rel(root, target string) (string, bool) {
	if !IsAbs(root) || !IsAbs(target) {
		return "", false
	}
	r, t := Clean(root), Clean(target)
	if strings.EqualFold(r, t) {
		return "", true
	}
	prefix := r
	if !strings.HasSuffix(prefix, `\`) {
		prefix += `\`
	}
	if len(t) <= len(prefix) || !strings.EqualFold(t[:len(prefix)], prefix) {
		return "", false
	}
	return strings.ReplaceAll(t[len(prefix):], `\`, "/"), true
}

// ErrNotUnderRoot is returned by WorkspaceRel for a directory outside the share root.
var ErrNotUnderRoot = errors.New("not under the share root")

// WorkspaceRel validates that dir can be a workspace under root and returns
// its forward-slash relative path. A workspace must lie strictly beneath the
// root (the root itself maps onto the VM account's home and would shadow it),
// and its first path component must not start with "." (that would shadow VM
// dot-directories such as .claude or .ssh).
func WorkspaceRel(root, dir string) (string, error) {
	rel, ok := Rel(root, dir)
	if !ok {
		return "", fmt.Errorf("%s: %w %s", dir, ErrNotUnderRoot, root)
	}
	if rel == "" {
		return "", fmt.Errorf("%s is the share root itself; open a project folder beneath it", dir)
	}
	if strings.HasPrefix(rel, ".") {
		return "", fmt.Errorf("%s: top-level folders starting with '.' under the share root cannot be workspaces", dir)
	}
	return rel, nil
}

// Join appends a forward-slash relative path to a Windows root, producing a
// cleaned backslash path.
func Join(root, rel string) string {
	if rel == "" {
		return Clean(root)
	}
	return Clean(root + `\` + rel)
}
