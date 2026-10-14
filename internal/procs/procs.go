// Package procs walks the Linux process tree via /proc. Elsewhere every
// function returns zero values.
package procs

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Ancestors returns pid and its ancestors, nearest first, stopping before
// init (pid 1). For an agent this is agent → shell → terminal emulator → …,
// which is what a UI needs to find the agent's terminal window.
func Ancestors(pid int) []int {
	var chain []int
	for i := 0; i < 32 && pid > 1; i++ {
		chain = append(chain, pid)
		pid = Parent(pid)
	}
	return chain
}

// Parent returns pid's parent, or 0 if unknown.
func Parent(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // comm may contain spaces and parens
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// Name returns the process's command name (/proc/<pid>/comm), or "".
func Name(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Alive reports whether a process with this pid exists.
func Alive(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}
