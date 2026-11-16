package discovery

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The OpenCode v2 service owns every session, but it runs under the user's
// service manager, not in a terminal: its process ancestry leads nowhere a
// UI can focus. The terminals belong to its clients, the TUIs connected to
// its port. This file finds them, and what each one is showing, so a
// session can be pointed at the terminal it is open in.

// Client is an OpenCode process connected to a server's port, typically a
// TUI talking to the shared v2 service.
type Client struct {
	PID     int
	Session string // the session it was started on (-s/--session), if any
	Dir     string // the directory it was started in
}

const tcpStateEstablished = "01"

// parseConnections returns the inodes of ESTABLISHED sockets whose remote
// end is port: the client side of connections to a local server on it.
func parseConnections(r io.Reader, port uint16) map[uint64]bool {
	out := map[uint64]bool{}
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || f[3] != tcpStateEstablished {
			continue
		}
		addr, p, ok := parseHostPort(f[2])
		if !ok || p != port || !addr.IsLoopback() {
			continue
		}
		if inode, err := strconv.ParseUint(f[9], 10, 64); err == nil && inode != 0 {
			out[inode] = true
		}
	}
	return out
}

// Clients returns the OpenCode processes, other than server itself, holding
// a loopback connection to port.
func (s *Scanner) Clients(server int, port uint16) []Client {
	conns := map[uint64]bool{}
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(s.ProcRoot, name))
		if err != nil {
			continue
		}
		for ino := range parseConnections(f, port) {
			conns[ino] = true
		}
		f.Close()
	}
	if len(conns) == 0 {
		return nil
	}
	var out []Client
	for _, p := range readProcesses(s.ProcRoot) {
		if p.PID == server || !s.Match(p) {
			continue
		}
		for _, ino := range socketInodes(s.ProcRoot, p.PID) {
			if conns[ino] {
				cwd, _ := os.Readlink(filepath.Join(s.ProcRoot, strconv.Itoa(p.PID), "cwd"))
				c := clientArgs(p.Args, cwd)
				c.PID = p.PID
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// clientArgs reads what an `opencode [flags] [directory]` command line says
// about the session and directory it opened. Flags that take a value are
// skipped with it, so their values are not mistaken for the directory.
func clientArgs(args []string, cwd string) Client {
	c := Client{Dir: cwd}
	if len(args) < 2 {
		return c
	}
	withValue := map[string]bool{"-s": true, "--session": true, "--server": true, "--prompt": true, "--log-level": true}
	var positional []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		switch {
		case name == "-s" || name == "--session":
			if hasVal {
				c.Session = val
			} else if i+1 < len(args) {
				i++
				c.Session = args[i]
			}
		case withValue[name] && !hasVal:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			positional = append(positional, a)
		}
	}
	// The directory is the last positional argument; earlier ones are
	// subcommands (`opencode mini <dir>`).
	if n := len(positional); n > 0 {
		dir := positional[n-1]
		if !filepath.IsAbs(dir) {
			if cwd == "" {
				return c
			}
			dir = filepath.Join(cwd, dir)
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			c.Dir = filepath.Clean(dir)
		}
	}
	return c
}

// ClientFor picks the client showing a session: the one started on it, or
// else the only client started in its directory and not on another
// session. 0 when none fits or the guess would be ambiguous.
func ClientFor(clients []Client, session, dir string) int {
	for _, c := range clients {
		if c.Session != "" && c.Session == session {
			return c.PID
		}
	}
	if dir == "" {
		return 0
	}
	dir = filepath.Clean(dir)
	pid := 0
	for _, c := range clients {
		if c.Session != "" || c.Dir == "" || filepath.Clean(c.Dir) != dir {
			continue
		}
		if pid != 0 {
			return 0
		}
		pid = c.PID
	}
	return pid
}
