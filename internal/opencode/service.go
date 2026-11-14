package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rgolangh/agent-mission-control/internal/paths"
)

// OpenCode v2 runs one shared background service that owns every session,
// listening on a loopback port protected by basic auth. The password lives
// in the user's config, next to nothing else: ~/.config/opencode/service.json
// ({"password": ...}, mode 0600). That file is what this adapter reads; it
// replaces the v1 bridge plugin, which existed only to give a plain
// `opencode` (no TCP port) an authenticated door.

// ServicePassword reads the basic-auth password of the user's OpenCode v2
// shared service. It refuses a file other users could read.
func ServicePassword() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, ".config", "opencode", "service.json")
	s, err := paths.ReadSecret(p)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(s), &cfg); err != nil || cfg.Password == "" {
		return "", fmt.Errorf("malformed %s", p)
	}
	return cfg.Password, nil
}

// VerifyService proves that url is the OpenCode v2 service owned by pid
// before the daemon sends it the service password. A same-user impostor
// could name its process "opencode" and listen on another port to harvest
// the password, so /api/info's own report of its pid must match the process
// that owns the listening socket: only the real service in that process can
// answer that way.
func VerifyService(ctx context.Context, url, password string, pid int) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/info", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth("opencode", password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var info struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&info); err != nil {
		return false
	}
	return info.Version != "" && info.PID == pid
}
