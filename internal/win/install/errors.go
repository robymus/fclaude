package install

import (
	"fmt"
	"strings"
)

// toolError is a failed helper program (icacls, powershell, ...).
type toolError struct {
	tool, output string
	err          error
}

func (e *toolError) Error() string {
	if s := strings.TrimSpace(e.output); s != "" {
		return fmt.Sprintf("%s: %s", e.tool, s)
	}
	return fmt.Sprintf("%s: %v", e.tool, e.err)
}

func (e *toolError) Unwrap() error { return e.err }
