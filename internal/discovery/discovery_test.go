package discovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// fakeProc builds a throwaway /proc tree.
type fakeProc struct {
	t    *testing.T
	root string
	rows []string
}

func newFakeProc(t *testing.T) *fakeProc {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeProc{t: t, root: root}
}

// listen adds a LISTEN row for 127.0.0.1:port and, for pid, an fd pointing at
// the row's inode.
func (f *fakeProc) listen(pid int, comm string, port int, inode uint64) {
	f.t.Helper()
	f.rows = append(f.rows, fmt.Sprintf("   0: 0100007F:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 %d 1 0000000000000000 100 0 0 10 0\n", port, inode))
	f.process(pid, comm)
	fd := filepath.Join(f.root, strconv.Itoa(pid), "fd")
	os.MkdirAll(fd, 0o755)
	if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(fd, strconv.Itoa(len(f.rows)+10))); err != nil {
		f.t.Fatal(err)
	}
	f.flush()
}

func (f *fakeProc) process(pid int, comm string) {
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmdline"), []byte(comm+"\x00--port\x00123\x00"), 0o644)
}

func (f *fakeProc) flush() {
	os.WriteFile(filepath.Join(f.root, "net", "tcp"), []byte(tcpHeader+strings.Join(f.rows, "")), 0o644)
}

func (f *fakeProc) dropAll() {
	f.rows = nil
	f.flush()
}

func (f *fakeProc) scanner() *Scanner {
	s := New()
	s.ProcRoot = f.root
	return s
}

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

func healthy(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"healthy":true,"version":%q}`, version)
	})
}

func TestScanFindsVerifiedOpenCode(t *testing.T) {
	srv := httptest.NewServer(healthy("1.18.30"))
	defer srv.Close()
	f := newFakeProc(t)
	f.listen(4242, "opencode", portOf(t, srv), 9001)

	got := f.scanner().Scan(context.Background())
	if len(got) != 1 {
		t.Fatalf("endpoints = %+v", got)
	}
	want := fmt.Sprintf("http://127.0.0.1:%d", portOf(t, srv))
	if got[0].URL != want || got[0].PID != 4242 || got[0].Version != "1.18.30" || got[0].AuthRequired {
		t.Errorf("endpoint = %+v, want url %s pid 4242 v1.18.30", got[0], want)
	}
}

func TestScanIgnoresHealthyPortOwnedByOtherProcess(t *testing.T) {
	// Answers like OpenCode, but the owner is not OpenCode: must be ignored.
	srv := httptest.NewServer(healthy("1.18.30"))
	defer srv.Close()
	f := newFakeProc(t)
	f.listen(777, "nginx", portOf(t, srv), 9002)

	if got := f.scanner().Scan(context.Background()); len(got) != 0 {
		t.Errorf("non-opencode owner was reported: %+v", got)
	}
}

func TestScanRejectsOpenCodeOwnedPortThatIsNotTheAPIServer(t *testing.T) {
	cases := map[string]http.Handler{
		"404":           http.NotFoundHandler(),
		"html":          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>hi</html>") }),
		"wrong shape":   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"ok":true}`) }),
		"unhealthy":     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"healthy":false,"version":"1"}`) }),
		"no version":    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"healthy":true}`) }),
		"500":           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", 500) }),
		"redirect away": http.RedirectHandler("http://example.invalid/", http.StatusFound),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			f := newFakeProc(t)
			f.listen(4242, "opencode", portOf(t, srv), 9003)
			if got := f.scanner().Scan(context.Background()); len(got) != 0 {
				t.Errorf("accepted: %+v", got)
			}
		})
	}
}

func TestScanTreatsUnauthorizedAsProtectedOpenCodeAndSendsNoCredentials(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="opencode"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	f := newFakeProc(t)
	f.listen(4242, "opencode", portOf(t, srv), 9004)

	got := f.scanner().Scan(context.Background())
	if len(got) != 1 || !got[0].AuthRequired || got[0].Version != "" {
		t.Fatalf("endpoints = %+v, want one AuthRequired endpoint", got)
	}
	if sawAuth {
		t.Error("discovery sent an Authorization header")
	}

	// ...but a 401 from a process that is NOT opencode is still ignored.
	f2 := newFakeProc(t)
	f2.listen(999, "sshd", portOf(t, srv), 9005)
	if got := f2.scanner().Scan(context.Background()); len(got) != 0 {
		t.Errorf("401 from non-opencode owner accepted: %+v", got)
	}
}

func TestScanMatchesInterpreterWrappedOpenCode(t *testing.T) {
	srv := httptest.NewServer(healthy("1.0.0"))
	defer srv.Close()
	f := newFakeProc(t)
	f.listen(55, "node", portOf(t, srv), 9006)
	os.WriteFile(filepath.Join(f.root, "55", "cmdline"), []byte("node\x00/usr/lib/node_modules/opencode\x00--port\x001\x00"), 0o644)

	if got := f.scanner().Scan(context.Background()); len(got) != 1 {
		t.Errorf("node-wrapped opencode not found: %+v", got)
	}
}

func TestScanFindsMultipleServers(t *testing.T) {
	a := httptest.NewServer(healthy("1.18.30"))
	b := httptest.NewServer(healthy("1.18.31"))
	defer a.Close()
	defer b.Close()
	f := newFakeProc(t)
	f.listen(10, "opencode", portOf(t, a), 9010)
	f.listen(11, "opencode", portOf(t, b), 9011)

	if got := f.scanner().Scan(context.Background()); len(got) != 2 {
		t.Errorf("endpoints = %+v, want 2", got)
	}
}

func TestParseListeners(t *testing.T) {
	tcp := tcpHeader +
		"   0: 0100007F:1000 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 148554 1 0 100 0 0 10 0\n" +
		// established, not listening
		"   1: 0100007F:1000 0100007F:9999 01 00000000:00000000 00:00000000 00000000  1000        0 200000 1 0 100 0 0 10 0\n" +
		// wildcard bind
		"   2: 00000000:1068 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 162868 1 0 100 0 0 10 0\n" +
		// garbage
		"   3: nonsense\n"
	got := parseListeners(strings.NewReader(tcp))
	if len(got) != 2 {
		t.Fatalf("listeners = %+v", got)
	}
	if got[0].Addr.String() != "127.0.0.1" || got[0].Port != 4096 || got[0].Inode != 148554 {
		t.Errorf("first = %+v", got[0])
	}
	if !got[1].Addr.IsUnspecified() || got[1].Port != 4200 {
		t.Errorf("second = %+v", got[1])
	}

	tcp6 := tcpHeader +
		"   0: 00000000000000000000000001000000:1000 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 300 1 0 100 0 0 10 0\n" +
		"   1: 00000000000000000000000000000000:1000 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 301 1 0 100 0 0 10 0\n"
	got = parseListeners(strings.NewReader(tcp6))
	if len(got) != 2 || got[0].Addr.String() != "::1" || !got[1].Addr.IsUnspecified() {
		t.Errorf("tcp6 listeners = %+v", got)
	}
	if probeHost(got[0].Addr) != "::1" || probeHost(got[1].Addr) != "::1" {
		t.Errorf("probeHost = %q %q", probeHost(got[0].Addr), probeHost(got[1].Addr))
	}
}

func TestCanonical(t *testing.T) {
	same := []string{"http://localhost:4096", "http://127.0.0.1:4096", "http://127.0.0.1:4096/", "http://[::1]:4096", "http://LOCALHOST:4096"}
	for _, u := range same {
		if Canonical(u) != Canonical(same[0]) {
			t.Errorf("Canonical(%q) = %q, want %q", u, Canonical(u), Canonical(same[0]))
		}
	}
	if Canonical("http://localhost:4096") == Canonical("http://localhost:4097") {
		t.Error("different ports collapsed")
	}
	if Canonical("http://10.0.0.5:4096") == Canonical("http://127.0.0.1:4096") {
		t.Error("remote host collapsed into loopback")
	}
}

func TestTrackerDebouncesRemovalAndDetectsRestart(t *testing.T) {
	a := Endpoint{URL: "http://127.0.0.1:4097", PID: 1}
	b := Endpoint{URL: "http://127.0.0.1:4098", PID: 2}
	tr := newTracker(2)

	kinds := func(cs []Change) string {
		var s []string
		for _, c := range cs {
			s = append(s, fmt.Sprintf("%s:%d", c.Kind, c.Endpoint.PID))
		}
		return strings.Join(s, ",")
	}

	if got := kinds(tr.update([]Endpoint{a, b})); got != "added:1,added:2" && got != "added:2,added:1" {
		t.Errorf("initial = %q", got)
	}
	if got := kinds(tr.update([]Endpoint{a, b})); got != "" {
		t.Errorf("steady state emitted %q", got)
	}
	// b misses one scan (slow probe): no removal yet.
	if got := kinds(tr.update([]Endpoint{a})); got != "" {
		t.Errorf("single miss emitted %q", got)
	}
	// b is back: the miss counter resets.
	tr.update([]Endpoint{a, b})
	if got := kinds(tr.update([]Endpoint{a})); got != "" {
		t.Errorf("miss counter not reset, emitted %q", got)
	}
	// second consecutive miss: removed.
	if got := kinds(tr.update([]Endpoint{a})); got != "removed:2" {
		t.Errorf("after 2 misses = %q", got)
	}
	// Same port, new PID (restart): old goes away after debounce, new is added at once.
	a2 := Endpoint{URL: "http://127.0.0.1:4097", PID: 9}
	if got := kinds(tr.update([]Endpoint{a2})); got != "added:9" {
		t.Errorf("restart add = %q", got)
	}
	if got := kinds(tr.update([]Endpoint{a2})); got != "removed:1" {
		t.Errorf("restart remove = %q", got)
	}
}

func TestWatchEmitsAddedThenRemoved(t *testing.T) {
	srv := httptest.NewServer(healthy("1.18.30"))
	defer srv.Close()
	f := newFakeProc(t)
	f.listen(4242, "opencode", portOf(t, srv), 9020)
	s := f.scanner()
	s.MissesToDrop = 2

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := s.Watch(ctx, 20*time.Millisecond)

	next := func() Change {
		t.Helper()
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatal("channel closed early")
			}
			return c
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for a change")
		}
		return Change{}
	}

	if c := next(); c.Kind != Added || c.Endpoint.PID != 4242 {
		t.Fatalf("first change = %+v", c)
	}
	f.dropAll() // the server's socket disappears
	if c := next(); c.Kind != Removed || c.Endpoint.PID != 4242 {
		t.Fatalf("second change = %+v", c)
	}
	cancel()
	for range ch { // drains and must close
	}
}
