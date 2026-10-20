package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLeavesHyprlandConfigAloneWithoutShortcutOption(t *testing.T) {
	e, _, _ := testEnv(t)
	path := filepath.Join(e.Home, ".config", "hypr", "bindings.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "-- user's Hyprland bindings\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(e, "v1", Options{Only: map[string]bool{"omarchy": true}}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != original {
		t.Fatalf("install changed Hyprland config without the opt-in: %q", got)
	}
}

func TestHyprlandKeybindAddsManagedOmarchyLuaBlockAndUninstallPreservesEdits(t *testing.T) {
	e, ran, out := testEnv(t, "omarchy-shell", "hyprctl")
	path := filepath.Join(e.Home, ".config", "hypr", "bindings.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "-- personal bindings\no.bind(\"SUPER + Q\", \"Close\", hl.dsp.window.close())\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	opt := Options{Only: map[string]bool{"omarchy": true}, HyprlandKeybind: true}
	if err := Install(e, "v1", opt); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	installed := read(t, path)
	if !strings.Contains(installed, `hl.dsp.global("org.rgolangh.agent-mission-control:toggle-panel")`) {
		t.Fatalf("global Quickshell binding missing:\n%s", installed)
	}
	if strings.Count(installed, "mission-control managed global shortcut") != 2 {
		t.Fatalf("expected one marked binding block:\n%s", installed)
	}
	calls := strings.Join(*ran, "\n")
	if strings.Index(calls, "omarchy-shell -q shell rescanPlugins") < 0 || strings.Index(calls, "omarchy-shell -q shell rescanPlugins") > strings.Index(calls, "hyprctl reload") {
		t.Fatalf("expected plugin rescan before Hyprland reload: %v", *ran)
	}

	// Reinstalling is idempotent.
	out.Reset()
	if err := Install(e, "v1", opt); err != nil {
		t.Fatalf("reinstall: %v\n%s", err, out)
	}
	if got := read(t, path); got != installed {
		t.Fatalf("reinstall changed the binding file:\n%s", got)
	}

	// Later user changes outside the marked block survive uninstall.
	userEdit := `o.bind("SUPER + CTRL + Q", "Personal action", "my-command")` + "\n"
	if err := os.WriteFile(path, []byte(installed+userEdit), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "mission-control managed global shortcut") || !strings.Contains(got, original) || !strings.Contains(got, userEdit) {
		t.Fatalf("uninstall should remove only its block and preserve edits:\n%s", got)
	}
}

func TestHyprlandKeybindUsesPlainHyprlandConfigSyntax(t *testing.T) {
	e, _, out := testEnv(t)
	path := filepath.Join(e.Home, ".config", "hypr", "bindings.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# personal bindings\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(e, "v1", Options{Only: map[string]bool{"omarchy": true}, HyprlandKeybind: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(read(t, path), "bind = SUPER ALT, M, global, org.rgolangh.agent-mission-control:toggle-panel") {
		t.Fatalf("plain Hyprland global binding missing:\n%s", read(t, path))
	}
}

func TestHyprlandKeybindDryRunDoesNotModifyConfig(t *testing.T) {
	e, _, _ := testEnv(t)
	path := filepath.Join(e.Home, ".config", "hypr", "bindings.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "-- keep my config unchanged\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Only: map[string]bool{"omarchy": true}, HyprlandKeybind: true, DryRun: true}
	if err := Install(e, "v1", opt); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != original {
		t.Fatalf("dry run changed Hyprland config: %q", got)
	}
	if _, err := os.Stat(e.ManifestPath()); err == nil {
		t.Fatal("dry run wrote an install manifest")
	}
}

func TestHyprlandKeybindRefusesSymlinkedConfig(t *testing.T) {
	e, _, _ := testEnv(t)
	path := filepath.Join(e.Home, ".config", "hypr", "bindings.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "bindings.lua")
	if err := os.WriteFile(outside, []byte("-- user config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	err := Install(e, "v1", Options{Only: map[string]bool{"omarchy": true}, HyprlandKeybind: true})
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	if got := read(t, outside); got != "-- user config\n" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}

func TestHyprlandKeybindRejectsChordConflictAndEditedBlocksNeedForceToRemove(t *testing.T) {
	e, _, _ := testEnv(t)
	path := filepath.Join(e.Home, ".config", "hypr", "bindings.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	conflict := `o.bind("SUPER + ALT + M", "Music", "spotify")` + "\n"
	if err := os.WriteFile(path, []byte(conflict), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Only: map[string]bool{"omarchy": true}, HyprlandKeybind: true}
	if err := Install(e, "v1", opt); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("expected conflict refusal, got %v", err)
	}
	if got := read(t, path); got != conflict {
		t.Fatalf("conflict must leave the config untouched: %q", got)
	}

	if err := os.WriteFile(path, []byte("-- personal bindings\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(e, "v1", opt); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(read(t, path), hyprlandShortcutAction, "org.example.edited:action", 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(e, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "org.example.edited:action") {
		t.Fatal("uninstall overwrote a user-edited managed block without --force")
	}
	if err := Uninstall(e, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "mission-control managed global shortcut") || !strings.Contains(got, "-- personal bindings") {
		t.Fatalf("forced uninstall should remove only the marked block:\n%s", got)
	}
}
