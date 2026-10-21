package agentmissioncontrol

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"testing"
)

func TestAssetsContainInstallables(t *testing.T) {
	want := []string{
		"integrations/opencode/agent-mission-control-bridge.js",
		"integrations/claude-code/.claude-plugin/plugin.json",
		"integrations/claude-code/hooks/hooks.json",
		"ui/omarchy/Widget.qml",
		"ui/omarchy/Api.qml",
		"ui/omarchy/manifest.json",
		"ui/gnome/metadata.json",
		"ui/gnome/extension.js",
		"ui/gnome/client.js",
		"ui/gnome/model.js",
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

// ui/omarchy is mirrored as its own repository (the plugin repo for
// `omarchy plugin add`), so it carries a copy of the license, and the
// desktop UIs must agree on the version (packaging/version.sh sets both).
func TestDesktopUIsCarryLicenseAndOneVersion(t *testing.T) {
	lic, _ := os.ReadFile("LICENSE")
	if cp, err := os.ReadFile("ui/omarchy/LICENSE"); err != nil || !bytes.Equal(lic, cp) {
		t.Errorf("ui/omarchy/LICENSE must be a copy of LICENSE (%v)", err)
	}
	var om struct{ Version string }
	var gn struct {
		Version string `json:"version-name"`
	}
	for p, v := range map[string]any{"ui/omarchy/manifest.json": &om, "ui/gnome/metadata.json": &gn} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if om.Version == "" || om.Version != gn.Version {
		t.Errorf("omarchy manifest version %q != GNOME metadata version-name %q", om.Version, gn.Version)
	}
}
