// Package terminal identifies the terminal tab an agent runs in, so a UI can
// bring that tab forward and not just the terminal's window. It is told by
// the terminal itself: the variables a terminal sets for every program it
// starts, read by a process running inside it (amc hook) each time it
// reports. Only what is needed to address the tab is kept.
package terminal

import (
	"strconv"
	"strings"
)

// Terminal addresses one tab (pane) of a terminal through the terminal's own
// control interface.
type Terminal struct {
	Kind    string `json:"kind"`    // which terminal: "kitty"
	Address string `json:"address"` // its control socket, e.g. "unix:/run/user/1000/kitty-123"
	Target  string `json:"target"`  // the tab or pane within it, e.g. a kitty window id
}

// Detect reads the terminal a process runs in from its environment (getenv,
// e.g. os.Getenv). nil if it is not in a terminal this package knows, or the
// terminal cannot be controlled from outside it.
func Detect(getenv func(string) string) *Terminal {
	// kitty: KITTY_WINDOW_ID names the window (pane) and so its tab;
	// KITTY_LISTEN_ON is set only when remote control listens on a socket.
	t := &Terminal{Kind: "kitty", Address: getenv("KITTY_LISTEN_ON"), Target: getenv("KITTY_WINDOW_ID")}
	if t.Valid() {
		return t
	}
	return nil
}

// Valid reports whether t is well-formed for its kind. A UI runs the
// terminal's control command with these values, so anything else is refused
// before it is stored.
func (t *Terminal) Valid() bool {
	if t == nil {
		return false
	}
	switch t.Kind {
	case "kitty":
		id, err := strconv.ParseUint(t.Target, 10, 32)
		return err == nil && id > 0 && strconv.FormatUint(id, 10) == t.Target && unixAddress(t.Address)
	}
	return false
}

// unixAddress accepts "unix:" followed by an absolute path or an abstract
// socket name ("@name") that fits a sockaddr_un, with no control characters.
func unixAddress(a string) bool {
	path, ok := strings.CutPrefix(a, "unix:")
	if !ok || len(path) < 2 || len(path) > 107 || (path[0] != '/' && path[0] != '@') {
		return false
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
