package discovery

import (
	"bufio"
	"encoding/hex"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// This file reads Linux /proc directly (stdlib only, no shelling out to ss):
//
//	/proc/net/tcp{,6}          every TCP socket, with state and inode
//	/proc/<pid>/fd/*           symlinks like "socket:[148554]" -> the inode
//	/proc/<pid>/{comm,cmdline} who the process is
//
// Joining listening inodes to a process's fd table tells us which process
// owns which port. /proc/<pid>/fd is only readable for our own processes, so
// this naturally finds only the current user's servers. The address decoding
// below assumes a little-endian host (x86-64, arm64), which is what /proc
// prints native-endian words on.

// listener is one TCP socket in the LISTEN state.
type listener struct {
	Addr  netip.Addr
	Port  uint16
	Inode uint64
}

// ProcInfo is what we know about a process when deciding whether it is one
// of the agent servers we are looking for.
type ProcInfo struct {
	PID  int
	Comm string   // /proc/<pid>/comm
	Exe  string   // /proc/<pid>/exe target, empty if unreadable
	Args []string // /proc/<pid>/cmdline split on NUL
}

const tcpStateListen = "0A"

// parseListeners extracts LISTEN rows from the text of /proc/net/tcp or
// /proc/net/tcp6. The first line is a header. Malformed rows are skipped.
func parseListeners(r io.Reader) []listener {
	var out []listener
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// sl local rem st tx:rx tr:when retrnsmt uid timeout inode ...
		if len(f) < 10 || f[3] != tcpStateListen {
			continue
		}
		addr, port, ok := parseHostPort(f[1])
		if !ok {
			continue
		}
		inode, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		out = append(out, listener{Addr: addr, Port: port, Inode: inode})
	}
	return out
}

// parseHostPort decodes "0100007F:1000" (IPv4) or
// "00000000000000000000000001000000:0277" (IPv6, four little-endian words).
func parseHostPort(s string) (netip.Addr, uint16, bool) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	b, err := hex.DecodeString(host)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	switch len(b) {
	case 4:
		return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]}), uint16(port), true
	case 16:
		var a [16]byte
		for i := 0; i < 16; i += 4 {
			a[i], a[i+1], a[i+2], a[i+3] = b[i+3], b[i+2], b[i+1], b[i]
		}
		return netip.AddrFrom16(a).Unmap(), uint16(port), true
	}
	return netip.Addr{}, 0, false
}

// listListeners returns all LISTEN sockets, IPv4 and IPv6. Missing files
// (e.g. IPv6 disabled) are fine.
func listListeners(procRoot string) []listener {
	var out []listener
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, name))
		if err != nil {
			continue
		}
		out = append(out, parseListeners(f)...)
		f.Close()
	}
	return out
}

// readProcesses lists every numeric /proc/<pid> directory.
func readProcesses(procRoot string) []ProcInfo {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []ProcInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		p := ProcInfo{PID: pid}
		if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
			p.Comm = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			p.Args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		}
		if t, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			p.Exe = t
		}
		out = append(out, p)
	}
	return out
}

// socketInodes returns the socket inodes a process holds open. Unreadable
// (someone else's) processes yield nothing.
func socketInodes(procRoot string, pid int) []uint64 {
	dir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []uint64
	for _, e := range entries {
		t, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if rest, ok := strings.CutPrefix(t, "socket:["); ok {
			if n, err := strconv.ParseUint(strings.TrimSuffix(rest, "]"), 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}
