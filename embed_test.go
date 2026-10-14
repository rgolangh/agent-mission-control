package missioncontrol

import (
	"io/fs"
	"testing"
)

func TestAssetsContainInstallables(t *testing.T) {
	want := []string{
		"integrations/opencode/mission-control-bridge.js",
		"integrations/claude-code/.claude-plugin/plugin.json",
		"integrations/claude-code/hooks/hooks.json",
		"ui/omarchy/Widget.qml",
		"ui/omarchy/Api.qml",
		"ui/omarchy/manifest.json",
	}
	for _, p := range want {
		if _, err := fs.Stat(Assets, p); err != nil {
			t.Errorf("missing embedded asset %s: %v", p, err)
		}
	}
}
