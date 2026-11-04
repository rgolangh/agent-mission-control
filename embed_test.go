package agentmissioncontrol

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// The package's copies (packaging/install-share.sh, linked by `amc install`
// in packaged mode) must be exactly what the binary embeds, so a packaged
// install and a per-user one put the same files in place.
func TestPackagedShareMatchesEmbeddedAssets(t *testing.T) {
	dest := t.TempDir()
	if out, err := exec.Command("packaging/install-share.sh", dest).CombinedOutput(); err != nil {
		t.Fatalf("install-share.sh: %v\n%s", err, out)
	}
	embedded := map[string]string{ // share dir -> asset dir
		"opencode":    "integrations/opencode",
		"claude-code": "integrations/claude-code",
		"omarchy":     "ui/omarchy",
	}
	got := map[string]bool{}
	filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dest, p)
			got[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for share, src := range embedded {
		fs.WalkDir(Assets, src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel := share + p[len(src):]
			want, _ := fs.ReadFile(Assets, p)
			if have, err := os.ReadFile(filepath.Join(dest, rel)); err != nil || !bytes.Equal(have, want) {
				t.Errorf("%s: not installed as embedded (%v)", rel, err)
			}
			delete(got, rel)
			return nil
		})
	}
	for rel := range got {
		t.Errorf("%s: installed but not embedded", rel)
	}
}
