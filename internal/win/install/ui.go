package install

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ConsoleUI is the wizard's text interface.
type ConsoleUI struct {
	in  *bufio.Reader
	out io.Writer
	// lastMB tracks download progress output (one line per 50 MB).
	lastMB int64
}

// NewConsoleUI reads answers from in and writes to out.
func NewConsoleUI(in io.Reader, out io.Writer) *ConsoleUI {
	return &ConsoleUI{in: bufio.NewReader(in), out: out}
}

// Step announces a numbered step.
func (u *ConsoleUI) Step(n, total int, title string) {
	_, _ = fmt.Fprintf(u.out, "\n[%d/%d] %s\n", n, total, title)
}

// Info prints a line.
func (u *ConsoleUI) Info(format string, a ...any) {
	_, _ = fmt.Fprintf(u.out, "  "+format+"\n", a...)
}

// Warn prints a highlighted line.
func (u *ConsoleUI) Warn(format string, a ...any) {
	_, _ = fmt.Fprintf(u.out, "  ! "+format+"\n", a...)
}

// ErrNoInput means stdin closed while a question was open.
var ErrNoInput = errors.New("no answer (input closed)")

func (u *ConsoleUI) readLine() (string, error) {
	line, err := u.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			return "", ErrNoInput
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Ask prompts for a value; an empty answer takes def.
func (u *ConsoleUI) Ask(prompt, def string) (string, error) {
	if def == "" {
		_, _ = fmt.Fprintf(u.out, "  %s: ", prompt)
	} else {
		_, _ = fmt.Fprintf(u.out, "  %s [%s]: ", prompt, def)
	}
	s, err := u.readLine()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

// Confirm asks a yes/no question; an empty answer takes def.
func (u *ConsoleUI) Confirm(prompt string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		_, _ = fmt.Fprintf(u.out, "  %s [%s]: ", prompt, hint)
		s, err := u.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// Progress reports bytes done (total 0 = unknown), one line per 50 MB.
func (u *ConsoleUI) Progress(label string, done, _ int64) {
	mb := done >> 20
	if mb/50 > u.lastMB/50 {
		_, _ = fmt.Fprintf(u.out, "  %s: %d MB\n", label, mb)
	}
	u.lastMB = mb
}

// Pause waits for Enter, so a double-clicked setup does not close its window
// before the result can be read.
func (u *ConsoleUI) Pause() {
	_, _ = fmt.Fprint(u.out, "\nPress Enter to close this window.")
	_, _ = u.readLine()
}
