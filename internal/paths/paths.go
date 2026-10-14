// Package paths locates mission-control's per-user runtime files. They live
// in $XDG_RUNTIME_DIR (/run/user/<uid>, mode 0700): only this user can reach
// them, so the Unix socket and token files need no further authentication,
// and nobody else can squat the socket before the daemon starts.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// RuntimeDir is $XDG_RUNTIME_DIR/mission-control. Without XDG_RUNTIME_DIR
// (no systemd login session) it falls back to ~/.cache/mission-control/run.
func RuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "mission-control")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "mission-control", "run")
}

// Socket is the daemon's Unix socket: CLI, hooks and MCP talk to it.
func Socket() string { return filepath.Join(RuntimeDir(), "api.sock") }

// Token is the bearer token the daemon requires on its TCP port (used by
// clients that can't speak to a Unix socket, like the QML widget).
func Token() string { return filepath.Join(RuntimeDir(), "token") }

// BridgeToken is where the OpenCode bridge plugin running in process pid
// writes the token its port requires.
func BridgeToken(pid int) string {
	return filepath.Join(RuntimeDir(), "opencode", fmt.Sprintf("%d.token", pid))
}

// EnsureRuntimeDir creates RuntimeDir with mode 0700 and refuses a
// directory that is not owned by this user or is accessible to others.
func EnsureRuntimeDir() error {
	d := RuntimeDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	st, err := os.Stat(d)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible to other users (mode %v); refusing to use it", d, st.Mode().Perm())
	}
	return nil
}

// ReadSecret reads a token file, refusing one that others could read.
func ReadSecret(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by other users (mode %v); ignoring it", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s, nil
}
