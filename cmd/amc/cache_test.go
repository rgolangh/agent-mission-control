package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCodexDefaultSocketUsesCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	got, err := codexDefaultSocket()
	if err != nil || got != filepath.Join(home, "app-server-control", "app-server-control.sock") {
		t.Fatalf("socket %q: %v", got, err)
	}
}

func TestCacheWarningLead(t *testing.T) {
	for _, tc := range []struct {
		lifetime, lead, want time.Duration
		invalid              bool
	}{
		{30 * time.Minute, 5 * time.Minute, 25 * time.Minute, false},
		{30 * time.Minute, 0, 0, false},
		{30 * time.Minute, 30 * time.Minute, 0, true},
		{30 * time.Minute, -time.Minute, 0, true},
		{0, 0, 0, true},
	} {
		got, err := cacheWarningAfter(tc.lifetime, tc.lead)
		if (err != nil) != tc.invalid || got != tc.want {
			t.Errorf("lifetime %s lead %s: got %s, %v", tc.lifetime, tc.lead, got, err)
		}
	}
}
