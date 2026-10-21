package install

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func testEnv(t *testing.T, tools ...string) (Env, *[]string, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "amc")
	os.WriteFile(exe, []byte("BINARY v1"), 0o755)
	assets := fstest.MapFS{
		"integrations/opencode/agent-mission-control-bridge.js": {Data: []byte("bridge v1")},
		"integrations/claude-code/.claude-plugin/plugin.json":   {Data: []byte(`{"name":"agent-mission-control"}`)},
		"integrations/claude-code/hooks/hooks.json":             {Data: []byte(`{"hooks":{}}`)},
		"ui/omarchy/Widget.qml":                                 {Data: []byte("Item {}")},
	}
	var ran []string
	out := &bytes.Buffer{}
	have := map[string]bool{"systemctl": true}
	for _, tl := range tools {
		have[tl] = true
	}
	return Env{
		Home: home, Exe: exe, Assets: assets, Out: out,
		Look: func(c string) bool { return have[c] },
		Run:  func(n string, a ...string) error { ran = append(ran, n+" "+strings.Join(a, " ")); return nil },
	}, &ran, out
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestInstallDetectedComponentsAndUninstall(t *testing.T) {
	e, ran, out := testEnv(t, "opencode", "claude")
	if err := Install(e, "v1", Options{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	h := e.Home
	if read(t, filepath.Join(h, ".local/bin/amc")) != "BINARY v1" {
		t.Error("binary not installed")
	}
	if !strings.Contains(read(t, filepath.Join(h, ".config/systemd/user/agent-mission-control.service")), "ExecStart="+filepath.Join(h, ".local/bin/amc")+" serve -discover") {
		t.Error("unit should run the installed binary")
	}
	if read(t, filepath.Join(h, ".config/opencode/plugins/agent-mission-control-bridge.js")) != "bridge v1" {
		t.Error("opencode plugin")
	}
	if read(t, filepath.Join(h, ".claude/skills/agent-mission-control/.claude-plugin/plugin.json")) == "" {
		t.Error("claude plugin (dot directory must be included)")
	}
	if _, err := os.Stat(filepath.Join(h, ".config/omarchy")); err == nil {
		t.Error("omarchy was not detected and must not be installed")
	}
	if !strings.Contains(strings.Join(*ran, "\n"), "systemctl --user enable agent-mission-control.service") {
		t.Errorf("service not enabled: %v", *ran)
	}

	// Reinstall is a no-op for unchanged files.
	out.Reset()
	if err := Install(e, "v1", Options{}); err != nil || regexp.MustCompile(`(?m)^\s+(new|update)\s{2,}`).MatchString(out.String()) {
		t.Errorf("reinstall: %v\n%s", err, out)
	}

	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".local/bin/amc", ".config/opencode/plugins/agent-mission-control-bridge.js", ".claude/skills/agent-mission-control", ".local/share/agent-mission-control"} {
		if _, err := os.Stat(filepath.Join(h, p)); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
	if _, err := os.Stat(filepath.Join(h, ".claude/skills")); err != nil {
		t.Error("uninstall must not remove shared parent directories")
	}
}

func TestInstallRefusesForeignAndModifiedFilesButUpdatesOurs(t *testing.T) {
	e, _, out := testEnv(t, "opencode")
	plugin := filepath.Join(e.Home, ".config/opencode/plugins/agent-mission-control-bridge.js")
	os.MkdirAll(filepath.Dir(plugin), 0o755)
	os.WriteFile(plugin, []byte("someone else's"), 0o644)
	if err := Install(e, "v1", Options{}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("foreign file must block install: %v", err)
	}
	if read(t, plugin) != "someone else's" {
		t.Fatal("a refused install must change nothing")
	}
	os.Remove(plugin)
	if err := Install(e, "v1", Options{}); err != nil {
		t.Fatal(err, out)
	}

	// An upgrade replaces our unchanged file...
	e.Assets.(fstest.MapFS)["integrations/opencode/agent-mission-control-bridge.js"].Data = []byte("bridge v2")
	if err := Install(e, "v2", Options{}); err != nil || read(t, plugin) != "bridge v2" {
		t.Fatalf("upgrade: %v %q", err, read(t, plugin))
	}
	// ...but not one the user edited, unless forced.
	os.WriteFile(plugin, []byte("my local tweak"), 0o644)
	e.Assets.(fstest.MapFS)["integrations/opencode/agent-mission-control-bridge.js"].Data = []byte("bridge v3")
	if err := Install(e, "v3", Options{}); err == nil || read(t, plugin) != "my local tweak" {
		t.Fatalf("modified file must be kept: %v", err)
	}
	if err := Uninstall(e, Options{}); err != nil || read(t, plugin) != "my local tweak" {
		t.Fatalf("uninstall must keep a modified file: %v", err)
	}
	if err := Install(e, "v3", Options{Force: true}); err != nil || read(t, plugin) != "bridge v3" {
		t.Fatalf("force: %v", err)
	}
}

func TestSymlinkedDevSetupIsLeftAlone(t *testing.T) {
	e, _, _ := testEnv(t, "claude")
	repo := t.TempDir()
	link := filepath.Join(e.Home, ".claude/skills/agent-mission-control")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(repo, link)
	// Files under a symlinked directory resolve into the checkout: never write there.
	if err := Install(e, "v1", Options{Only: map[string]bool{"claude-code": true}}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(repo); len(entries) != 0 {
		t.Errorf("wrote into the symlinked checkout: %v", entries)
	}
}

// A plugin directory installed as files and later replaced by a symlink into
// a checkout (switching to a dev setup) must not lose the checkout's files
// to uninstall, even when their content matches what install wrote.
func TestUninstallNeverDeletesThroughASymlink(t *testing.T) {
	e, _, _ := testEnv(t, "claude")
	if err := Install(e, "v1", Options{Only: map[string]bool{"claude-code": true}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.Home, ".claude/skills/agent-mission-control")
	repo := t.TempDir()
	if err := os.CopyFS(repo, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	os.Symlink(repo, dir)
	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "hooks", "hooks.json")); err != nil {
		t.Errorf("uninstall deleted a checkout file through the symlink: %v", err)
	}
	if _, err := os.Stat(e.ManifestPath()); err == nil {
		t.Error("manifest still lists files uninstall left behind")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	e, ran, out := testEnv(t, "opencode", "claude")
	if err := Install(e, "v1", Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(e.Home); len(entries) != 0 || len(*ran) != 0 {
		t.Errorf("dry run touched %v / ran %v", entries, *ran)
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Error(out.String())
	}
}

// Regression: uninstalling only the GNOME extension once stopped and
// disabled the user's real daemon service.
func TestUninstallLeavesServiceAloneUnlessItInstalledIt(t *testing.T) {
	e, ran, _ := testEnv(t)
	e.Assets.(fstest.MapFS)["ui/gnome/metadata.json"] = &fstest.MapFile{Data: []byte("{}")}
	if err := Install(e, "v1", Options{Only: map[string]bool{"gnome": true}}); err != nil {
		t.Fatal(err)
	}
	*ran = nil
	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range *ran {
		if strings.HasPrefix(c, "systemctl") {
			t.Errorf("uninstall ran %q without having installed the service", c)
		}
	}
	// With the daemon installed, uninstall does stop it.
	if err := Install(e, "v1", Options{Only: map[string]bool{"daemon": true}}); err != nil {
		t.Fatal(err)
	}
	*ran = nil
	Uninstall(e, Options{})
	if !strings.Contains(strings.Join(*ran, "\n"), "systemctl --user disable --now agent-mission-control.service") {
		t.Errorf("service installed by us must be stopped: %v", *ran)
	}
}

// packagedEnv is testEnv with a distribution package installed under a
// fake root, and the running binary being the packaged one.
func packagedEnv(t *testing.T, tools ...string) (Env, *[]string, *bytes.Buffer) {
	t.Helper()
	e, ran, out := testEnv(t, tools...)
	e.Root = t.TempDir()
	e.Exe = e.sys(SystemBin)
	for p, data := range map[string]string{SystemBin: "BINARY pkg", SystemUnit: "unit", SystemGNOME + "/metadata.json": "{}"} {
		os.MkdirAll(filepath.Dir(e.sys(p)), 0o755)
		os.WriteFile(e.sys(p), []byte(data), 0o644)
	}
	e.Assets.(fstest.MapFS)["ui/gnome/metadata.json"] = &fstest.MapFile{Data: []byte("{}")}
	return e, ran, out
}

func TestPackagedInstallUsesPackagedServiceAndExtension(t *testing.T) {
	e, ran, out := packagedEnv(t, "gnome-shell")
	if !e.Packaged() {
		t.Fatal("not detected as packaged")
	}
	if err := Install(e, "v1", Options{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, p := range []string{".local/bin/amc", ".config/systemd/user/agent-mission-control.service", ".local/share/gnome-shell/extensions/" + GNOMEUUID} {
		if _, err := os.Stat(filepath.Join(e.Home, p)); err == nil {
			t.Errorf("%s written although the package provides it", p)
		}
	}
	if !strings.Contains(strings.Join(*ran, "\n"), "systemctl --user enable agent-mission-control.service") {
		t.Errorf("packaged service not enabled: %v", *ran)
	}
	*ran = nil
	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(*ran, "\n"), "systemctl --user disable --now agent-mission-control.service") {
		t.Errorf("uninstall must stop the packaged service it enabled: %v", *ran)
	}
	if _, err := os.Stat(e.ManifestPath()); err == nil {
		t.Error("manifest left behind")
	}
}

// A per-user install from a release binary, followed by the package: the
// per-user binary, unit and extension would shadow the packaged ones.
func TestPackagedInstallRemovesEarlierPerUserCopies(t *testing.T) {
	e, _, out := packagedEnv(t, "gnome-shell")
	user := e
	user.Exe = filepath.Join(t.TempDir(), "amc")
	os.WriteFile(user.Exe, []byte("BINARY v1"), 0o755)
	if err := Install(user, "v1", Options{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	unit := filepath.Join(e.Home, ".config/systemd/user/agent-mission-control.service")
	ext := filepath.Join(e.Home, ".local/share/gnome-shell/extensions", GNOMEUUID, "metadata.json")
	if read(t, unit) == "" || read(t, ext) == "" {
		t.Fatal("per-user install incomplete")
	}
	if err := Install(e, "v2", Options{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, p := range []string{filepath.Join(e.Home, ".local/bin/amc"), unit, filepath.Dir(ext)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still shadows the package", p)
		}
	}
	if m := e.LoadManifest(); m.Files[unit] != "" || m.Files[ext] != "" {
		t.Errorf("manifest still lists removed files: %v", m.Files)
	}
}

func TestPackagedInstallKeepsAUnitItDidNotWrite(t *testing.T) {
	e, _, out := packagedEnv(t)
	unit := filepath.Join(e.Home, ".config/systemd/user/agent-mission-control.service")
	os.MkdirAll(filepath.Dir(unit), 0o755)
	os.WriteFile(unit, []byte("mine"), 0o644)
	if err := Install(e, "v1", Options{Only: map[string]bool{"daemon": true}}); err != nil {
		t.Fatal(err)
	}
	if read(t, unit) != "mine" || !strings.Contains(out.String(), "shadows package") {
		t.Errorf("must keep and warn about a foreign unit:\n%s", out)
	}
}

// A dev build run from elsewhere is not the packaged binary: it installs
// per-user copies, which deliberately override the package's.
func TestDevBinaryWithPackageInstalledIsPerUser(t *testing.T) {
	e, _, _ := packagedEnv(t)
	e.Exe = filepath.Join(t.TempDir(), "amc")
	os.WriteFile(e.Exe, []byte("BINARY dev"), 0o755)
	if e.Packaged() {
		t.Fatal("a binary outside /usr/bin is not the packaged one")
	}
}

// `omarchy plugin add <mirror repo>` clones into the same directory install
// uses (named after the manifest id). That clone is omarchy's: install skips
// the component instead of refusing on foreign files, and uninstall leaves
// the clone alone even where its files match what install once wrote.
func TestOmarchyPluginAddedWithGitIsLeftToOmarchy(t *testing.T) {
	e, ran, out := testEnv(t, "omarchy-shell")
	if err := Install(e, "v1", Options{}); err != nil {
		t.Fatal(err, out)
	}
	widget := filepath.Join(e.Home, ".config/omarchy/plugins/agent-mission-control/Widget.qml")
	os.MkdirAll(filepath.Join(filepath.Dir(widget), ".git"), 0o755) // now a clone with the same content
	if !e.OmarchyGitManaged() {
		t.Fatal("git clone not detected")
	}
	os.WriteFile(widget, []byte("Item { /* newer, from git */ }"), 0o644)
	out.Reset()
	*ran = nil
	if err := Install(e, "v2", Options{Only: map[string]bool{"omarchy": true}}); err != nil {
		t.Fatalf("a git-managed plugin must not block install: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "omarchy plugin update") || len(*ran) != 0 {
		t.Errorf("want a skip note and no commands, got %v:\n%s", *ran, out)
	}
	os.WriteFile(widget, []byte("Item {}"), 0o644) // same bytes install wrote
	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	if read(t, widget) != "Item {}" {
		t.Error("uninstall removed a file from omarchy's clone")
	}
}

func TestPackagedUnitMatchesInstaller(t *testing.T) {
	b, err := os.ReadFile("../../packaging/systemd/agent-mission-control.service")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`\A(#.*\n)*`).ReplaceAllString(string(b), "")
	if body != UnitBody(SystemBin) {
		t.Errorf("packaging/systemd unit drifted from install.UnitBody:\n%s", body)
	}
}
