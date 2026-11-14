// Package discovery finds OpenCode servers already running on this machine so
// agent-mission-control can attach to them without being told their URLs.
//
// Method: map listening TCP sockets to the processes that own them (via
// /proc), keep only processes that look like OpenCode, then confirm each
// candidate really is an OpenCode API server before reporting it: a v1
// server with GET /global/health, a v2 server (the shared background
// service, which serves its web UI on unknown paths) with GET /api/info.
// Two independent checks matter because nothing else should ever receive
// the OpenCode password: the owning process must be OpenCode, and the port
// must answer like OpenCode.
//
// Limits: only listening servers can be found — a v1 `opencode` without
// --port does not listen (that is what the bridge plugin was for); v2 always
// does, through its shared service. Linux only (needs /proc).
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Endpoint is one verified OpenCode API server.
type Endpoint struct {
	URL     string // base URL, e.g. http://127.0.0.1:4096
	PID     int    // owning process
	Version string // from the health/info probe; empty if AuthRequired

	// API is the server's major API version: 1 (standalone `opencode serve`,
	// /global/health and /event) or 2 (the shared background service,
	// /api/info and /api/event).
	API int

	// AuthRequired means the identity probe was refused (401), so the server
	// could not be checked against the {healthy, version} shape. Its identity
	// then rests on the owning process alone. Credentials are never sent by
	// discovery: the caller supplies them, and should do so only for
	// endpoints returned here.
	AuthRequired bool
}

// Key identifies an endpoint across scans: same port and same process.
func (e Endpoint) Key() string { return fmt.Sprintf("%s#%d", Canonical(e.URL), e.PID) }

// Kind says whether a Change adds or removes an endpoint.
type Kind int

const (
	Added Kind = iota
	Removed
)

func (k Kind) String() string {
	if k == Added {
		return "added"
	}
	return "removed"
}

// Change is one endpoint appearing or disappearing.
type Change struct {
	Kind     Kind
	Endpoint Endpoint
}

// Scanner finds OpenCode servers. The zero value is not usable; call New.
// Fields exist so tests can point it at a fake /proc.
type Scanner struct {
	ProcRoot     string              // default "/proc"
	HTTP         *http.Client        // default: short timeout, no redirects
	Match        func(ProcInfo) bool // default IsOpenCode
	MissesToDrop int                 // scans an endpoint may go missing before Removed; default 2
}

func New() *Scanner {
	return &Scanner{
		ProcRoot: "/proc",
		HTTP: &http.Client{
			// Generous: the dev machine this was built on runs at load 80+.
			Timeout: 3 * time.Second,
			// Never follow a redirect off the probed port.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Match:        IsOpenCode,
		MissesToDrop: 2,
	}
}

// IsOpenCode reports whether a process looks like OpenCode: its name, its
// executable, or its first two arguments (to catch `node /path/opencode`)
// mention "opencode". Deliberately loose — the health check is the real gate.
func IsOpenCode(p ProcInfo) bool {
	names := []string{p.Comm, filepath.Base(p.Exe)}
	for i := 0; i < len(p.Args) && i < 2; i++ {
		names = append(names, filepath.Base(p.Args[i]))
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "opencode") {
			return true
		}
	}
	return false
}

type candidate struct {
	pid int
	url string
}

// Scan returns every verified OpenCode endpoint on this machine right now,
// sorted by URL.
func (s *Scanner) Scan(ctx context.Context) []Endpoint {
	listeners := listListeners(s.ProcRoot)
	if len(listeners) == 0 {
		return nil
	}
	byInode := make(map[uint64]listener, len(listeners))
	for _, l := range listeners {
		byInode[l.Inode] = l
	}

	// One candidate per (process, port): a dual-stack server shows up as two
	// sockets on the same port.
	seen := map[string]bool{}
	var cands []candidate
	for _, p := range readProcesses(s.ProcRoot) {
		if !s.Match(p) {
			continue
		}
		for _, ino := range socketInodes(s.ProcRoot, p.PID) {
			l, ok := byInode[ino]
			if !ok {
				continue
			}
			k := fmt.Sprintf("%d:%d", p.PID, l.Port)
			if seen[k] {
				continue
			}
			seen[k] = true
			cands = append(cands, candidate{p.PID, "http://" + net.JoinHostPort(probeHost(l.Addr), strconv.Itoa(int(l.Port)))})
		}
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []Endpoint
	)
	for _, c := range cands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ep, ok := s.probe(ctx, c); ok {
				mu.Lock()
				out = append(out, ep)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// probeHost picks the address to dial for a listening socket. A wildcard
// bind (0.0.0.0 / ::) is reached over loopback.
func probeHost(a netip.Addr) string {
	switch {
	case a.IsUnspecified() && a.Is4():
		return "127.0.0.1"
	case a.IsUnspecified():
		return "::1"
	default:
		return a.String()
	}
}

// probe checks one candidate without credentials. It never sends them.
// The v1 check runs first; a v2 server answers /global/health with its web
// UI's HTML, which fails the JSON check, and is then identified by /api/info.
func (s *Scanner) probe(ctx context.Context, c candidate) (Endpoint, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/global/health", nil)
	if err != nil {
		return Endpoint{}, false
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Endpoint{}, false
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		// A password-protected OpenCode. We can't read its health body, so
		// only the process check vouches for it.
		return Endpoint{URL: c.url, PID: c.pid, AuthRequired: true}, true
	case http.StatusOK:
		var h struct {
			Healthy bool   `json:"healthy"`
			Version string `json:"version"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&h); err != nil || !h.Healthy || h.Version == "" {
			// Not v1's health JSON: either something else entirely, or a v2
			// server whose SPA fallback answered HTML. /api/info tells them
			// apart without credentials.
			return s.probeV2(ctx, c)
		}
		return Endpoint{URL: c.url, PID: c.pid, Version: h.Version, API: 1}, true
	}
	return s.probeV2(ctx, c)
}

// probeV2 identifies a v2 server. /api/info needs the service password, but
// its refusal is as identifying as its answer: only an OpenCode v2 server
// answers 401 with an UnauthorizedError JSON body (a v1 server has no such
// route and returns 404). The version is read when the server allows it.
func (s *Scanner) probeV2(ctx context.Context, c candidate) (Endpoint, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/api/info", nil)
	if err != nil {
		return Endpoint{}, false
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Endpoint{}, false
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var info struct {
			Version string `json:"version"`
			PID     int    `json:"pid"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&info); err != nil || info.Version == "" || info.PID <= 0 {
			return Endpoint{}, false
		}
		return Endpoint{URL: c.url, PID: c.pid, Version: info.Version, API: 2}, true
	case http.StatusUnauthorized:
		var e struct {
			Tag string `json:"_tag"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e); err != nil || e.Tag != "UnauthorizedError" {
			return Endpoint{}, false
		}
		return Endpoint{URL: c.url, PID: c.pid, AuthRequired: true, API: 2}, true
	}
	return Endpoint{}, false
}

// Watch scans immediately and then every interval, emitting Added for each
// new endpoint and Removed once one has been missing for MissesToDrop
// consecutive scans. The debounce matters: a single slow or failed probe on
// a loaded machine must not tear down a healthy connection. A server that
// restarts on the same port under a new PID is reported as Added (new PID) at
// once and Removed (old PID) after the debounce. The channel closes when ctx
// is done.
func (s *Scanner) Watch(ctx context.Context, interval time.Duration) <-chan Change {
	out := make(chan Change)
	go func() {
		defer close(out)
		t := newTracker(s.MissesToDrop)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			for _, ch := range t.update(s.Scan(ctx)) {
				select {
				case out <- ch:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return out
}

// tracker turns successive scan results into Added/Removed changes.
type tracker struct {
	drop  int
	known map[string]*tracked
}

type tracked struct {
	ep     Endpoint
	misses int
}

func newTracker(missesToDrop int) *tracker {
	if missesToDrop < 1 {
		missesToDrop = 1
	}
	return &tracker{drop: missesToDrop, known: map[string]*tracked{}}
}

func (t *tracker) update(found []Endpoint) []Change {
	var changes []Change
	present := map[string]bool{}
	for _, ep := range t.onePerProcess(found) {
		k := ep.Key()
		present[k] = true
		if cur, ok := t.known[k]; ok {
			cur.misses, cur.ep = 0, ep
			continue
		}
		t.known[k] = &tracked{ep: ep}
		changes = append(changes, Change{Added, ep})
	}
	var gone []string
	for k, cur := range t.known {
		if present[k] {
			continue
		}
		if cur.misses++; cur.misses >= t.drop {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone) // deterministic order
	for _, k := range gone {
		changes = append(changes, Change{Removed, t.known[k].ep})
		delete(t.known, k)
	}
	return changes
}

// onePerProcess keeps one endpoint per OpenCode process. A process can
// listen on several ports (its own --port plus the agent-mission-control bridge
// plugin, which OpenCode may even load twice), and every one of them serves
// the same sessions: watching more than one would double every event and
// make the workflows' origin flap. The endpoint already being watched wins,
// so a port that appears later causes no reconnect; otherwise the lowest
// URL, for determinism. (If the watched port closes while the process lives,
// another is picked at once and the old one is removed after the usual
// debounce, so both are briefly watched.)
func (t *tracker) onePerProcess(found []Endpoint) []Endpoint {
	byPID := map[int][]Endpoint{}
	var pids []int
	for _, ep := range found {
		if _, ok := byPID[ep.PID]; !ok {
			pids = append(pids, ep.PID)
		}
		byPID[ep.PID] = append(byPID[ep.PID], ep)
	}
	out := make([]Endpoint, 0, len(pids))
	for _, pid := range pids {
		eps := byPID[pid]
		sort.Slice(eps, func(i, j int) bool { return eps[i].URL < eps[j].URL })
		pick := eps[0]
		for _, ep := range eps {
			if _, ok := t.known[ep.Key()]; ok {
				pick = ep
				break
			}
		}
		out = append(out, pick)
	}
	return out
}

// Canonical reduces a server URL to a comparable key so that
// http://localhost:4096, http://127.0.0.1:4096 and http://[::1]:4096/ are
// the same server. Used to dedupe discovered endpoints against explicit
// -opencode flags.
func Canonical(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimRight(rawURL, "/"))
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return "loopback:" + port
	}
	if a, err := netip.ParseAddr(host); err == nil && a.IsLoopback() {
		return "loopback:" + port
	}
	return net.JoinHostPort(host, port)
}
