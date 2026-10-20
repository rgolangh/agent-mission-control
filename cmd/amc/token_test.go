package main

import (
	"os"
	"testing"

	"github.com/rgolangh/agent-mission-control/internal/paths"
)

// While the daemon is down its port is free for anyone to bind, so the token
// file goes away with it; a newer daemon's token is left alone (threat model N4).
func TestTokenFileLivesOnlyAsLongAsTheDaemon(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if err := paths.EnsureRuntimeDir(); err != nil {
		t.Fatal(err)
	}
	old, err := writeToken()
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(paths.Token()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", st, err)
	}
	newer, _ := writeToken()
	removeToken(old)
	if got, _ := paths.ReadSecret(paths.Token()); got != newer {
		t.Error("removed another daemon's token")
	}
	removeToken(newer)
	if _, err := os.Stat(paths.Token()); !os.IsNotExist(err) {
		t.Errorf("token file still there after shutdown: %v", err)
	}
}
