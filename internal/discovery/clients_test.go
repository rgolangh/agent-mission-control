package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// connect adds an ESTABLISHED row from 127.0.0.1:local to 127.0.0.1:port and,
// for pid, an fd pointing at its inode; cmdline is pid's argv and cwd its
// working directory.
func (f *fakeProc) connect(pid int, cmdline []string, cwd string, local, port int, inode uint64) {
	f.t.Helper()
	f.rows = append(f.rows, fmt.Sprintf("   0: 0100007F:%04X 0100007F:%04X 01 00000000:00000000 00:00000000 00000000  1000        0 %d 1 0000000000000000 100 0 0 10 0\n", local, port, inode))
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	os.MkdirAll(filepath.Join(dir, "fd"), 0o755)
	os.WriteFile(filepath.Join(dir, "comm"), []byte(filepath.Base(cmdline[0])+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(cmdline, "\x00")+"\x00"), 0o644)
	if cwd != "" {
		os.Symlink(cwd, filepath.Join(dir, "cwd"))
	}
	if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(dir, "fd", strconv.Itoa(len(f.rows)+10))); err != nil {
		f.t.Fatal(err)
	}
	f.flush()
}

func TestClientsFindsTUIsConnectedToTheService(t *testing.T) {
	f := newFakeProc(t)
	work, other := t.TempDir(), t.TempDir()
	f.listen(100, "opencode", 4096, 1)
	f.connect(200, []string{"/x/opencode.exe", "-s", "ses_a"}, work, 50000, 4096, 2)
	f.connect(201, []string{"/x/opencode.exe", "."}, other, 50001, 4096, 3)
	f.connect(202, []string{"/x/opencode.exe"}, work, 50002, 4097, 4) // another server's client
	f.connect(203, []string{"/usr/bin/curl"}, work, 50003, 4096, 5)   // not OpenCode
	f.connect(100, []string{"/x/opencode.exe", "serve", "--service"}, "", 50004, 4096, 6)

	got := f.scanner().Clients(100, 4096)
	if len(got) != 2 {
		t.Fatalf("clients = %+v", got)
	}
	byPID := map[int]Client{}
	for _, c := range got {
		byPID[c.PID] = c
	}
	if c := byPID[200]; c.Session != "ses_a" || c.Dir != work {
		t.Errorf("pinned client = %+v", c)
	}
	if c := byPID[201]; c.Session != "" || c.Dir != other {
		t.Errorf("directory client = %+v", c)
	}
}

func TestClientArgs(t *testing.T) {
	cwd := t.TempDir()
	sub := filepath.Join(cwd, "sub")
	os.Mkdir(sub, 0o755)
	for _, tc := range []struct {
		args         []string
		session, dir string
	}{
		{[]string{"opencode"}, "", cwd},
		{[]string{"opencode", "--session", "ses_x"}, "ses_x", cwd},
		{[]string{"opencode", "--session=ses_x", "sub"}, "ses_x", sub},
		{[]string{"opencode", "-s", "ses_x", sub}, "ses_x", sub},
		{[]string{"opencode", "--prompt", "sub", "-c"}, "", cwd}, // a flag's value is not the directory
		{[]string{"opencode", "mini", "sub"}, "", sub},
		{[]string{"opencode", "run", "fix the bug"}, "", cwd}, // not a directory
	} {
		c := clientArgs(tc.args, cwd)
		if c.Session != tc.session || c.Dir != tc.dir {
			t.Errorf("clientArgs(%q) = %+v, want session %q dir %q", tc.args, c, tc.session, tc.dir)
		}
	}
}

func TestClientFor(t *testing.T) {
	clients := []Client{
		{PID: 1, Session: "ses_a", Dir: "/w"},
		{PID: 2, Dir: "/w"},
		{PID: 3, Dir: "/both"},
		{PID: 4, Dir: "/both"},
	}
	for _, tc := range []struct {
		session, dir string
		want         int
	}{
		{"ses_a", "/elsewhere", 1}, // started on it: wins over the directory
		{"ses_b", "/w/", 2},        // only unpinned client in its directory
		{"ses_c", "/both", 0},      // two candidates: don't guess
		{"ses_d", "/none", 0},
		{"ses_e", "", 0},
	} {
		if got := ClientFor(clients, tc.session, tc.dir); got != tc.want {
			t.Errorf("ClientFor(%q, %q) = %d, want %d", tc.session, tc.dir, got, tc.want)
		}
	}
}
