package procjob

import (
	"os"
	"strconv"
	"strings"
)

// processExists treats a zombie as gone: it has exited, merely unreaped.
func processExists(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || !strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
}
