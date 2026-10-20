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
		"ui/gnome/metadata.json",
		"ui/gnome/extension.js",
		"ui/gnome/client.js",
		"ui/gnome/model.js",
		"ui/gnome/schemas/org.gnome.shell.extensions.mission-control.gschema.xml",
		"ui/gnome/schemas/gschemas.compiled",
	}
	for _, p := range want {
		if _, err := fs.Stat(Assets, p); err != nil {
			t.Errorf("missing embedded asset %s: %v", p, err)
		}
	}
	// Tests and harnesses must not be installed.
	for _, p := range []string{"ui/gnome/test", "ui/omarchy/test"} {
		if _, err := fs.Stat(Assets, p); err == nil {
			t.Errorf("%s is embedded but must not be installed", p)
		}
	}
}
