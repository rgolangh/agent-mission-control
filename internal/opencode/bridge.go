package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/rgolangh/agent-mission-control/internal/paths"
)

// BridgeCredential reads the token the mission-control bridge plugin wrote
// for OpenCode process pid, and the port it protects. The file is only
// readable by this user (paths.ReadSecret refuses anything looser).
func BridgeCredential(pid int) (port int, token string, err error) {
	s, err := paths.ReadSecret(paths.BridgeToken(pid))
	if err != nil {
		return 0, "", err
	}
	var c struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(s), &c); err != nil || c.Port == 0 || c.Token == "" {
		return 0, "", fmt.Errorf("malformed bridge credential for pid %d", pid)
	}
	return c.Port, c.Token, nil
}
