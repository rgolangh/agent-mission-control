package install

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	hyprlandShortcutAppID  = "org.rgolangh.agent-mission-control"
	hyprlandShortcutName   = "toggle-panel"
	hyprlandShortcutAction = hyprlandShortcutAppID + ":" + hyprlandShortcutName
)

type ManagedAppend struct {
	Block  string `json:"block"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

type hyprlandBindingPlan struct {
	path    string
	before  []byte
	after   []byte
	append  ManagedAppend
	mode    fs.FileMode
	changed bool
	owned   bool
}

func planHyprlandBinding(e Env, manifest Manifest, force bool) (*hyprlandBindingPlan, error) {
	paths := []struct {
		path string
		lua  bool
	}{
		{filepath.Join(e.Home, ".config", "hypr", "bindings.lua"), true},
		{filepath.Join(e.Home, ".config", "hypr", "bindings.conf"), false},
		{filepath.Join(e.Home, ".config", "hypr", "hyprland.conf"), false},
	}

	var path string
	lua := false
	var info os.FileInfo
	for _, candidate := range paths {
		st, err := os.Lstat(candidate.path)
		if err == nil {
			path, lua, info = candidate.path, candidate.lua, st
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if path == "" {
		return nil, fmt.Errorf("--hyprland-keybind needs ~/.config/hypr/bindings.lua, bindings.conf, or hyprland.conf")
	}
	if info.Mode()&fs.ModeSymlink != 0 || underSymlink(e.Home, path) {
		return nil, fmt.Errorf("refusing to edit symlinked Hyprland config %s; add the shortcut manually", tilde(e, path))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Hyprland config %s is not a regular file", tilde(e, path))
	}

	before, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	eol := "\n"
	if bytes.Contains(before, []byte("\r\n")) {
		eol = "\r\n"
	}
	block, begin, end := hyprlandBindingBlock(lua, eol)
	separator := ""
	if len(before) > 0 {
		separator = eol
		if !bytes.HasSuffix(before, []byte(eol)) {
			separator += eol
		}
	}
	managed := ManagedAppend{Block: block, Prefix: separator, Suffix: eol}
	plan := &hyprlandBindingPlan{path: path, before: before, mode: info.Mode().Perm(), append: managed}
	if plan.mode == 0 {
		plan.mode = 0o644
	}

	if old, ok := manifest.Appends[path]; ok {
		segment := appendSegment(old)
		if bytes.Count(before, []byte(segment)) == 1 {
			plan.after = bytes.Replace(before, []byte(segment), []byte(appendSegment(managed)), 1)
			plan.changed = !bytes.Equal(plan.after, before)
			plan.owned = true
			return plan, nil
		}
		if !bytes.Contains(before, []byte(begin)) && !bytes.Contains(before, []byte(end)) && !bytes.Contains(before, []byte(hyprlandShortcutAction)) {
			// The user removed the binding block; re-adding it does not overwrite
			// any of their remaining Hyprland config.
			managed.Prefix = separatorFor(before, eol)
			plan.append = managed
			plan.after = appendBlock(before, managed)
			plan.changed = true
			plan.owned = true
			return plan, nil
		}
		if !force {
			return nil, fmt.Errorf("managed Mission Control binding in %s was edited; inspect it or reinstall with --force", tilde(e, path))
		}
		base, found := removeMarkedBinding(before, old, begin, end)
		if !found {
			return nil, fmt.Errorf("could not find the managed Mission Control binding in %s", tilde(e, path))
		}
		managed.Prefix = separatorFor(base, eol)
		plan.append = managed
		plan.after = appendBlock(base, managed)
		plan.changed = !bytes.Equal(plan.after, before)
		plan.owned = true
		return plan, nil
	}

	if bytes.Contains(before, []byte(hyprlandShortcutAction)) {
		// A manually configured binding already targets this Quickshell action;
		// leave it user-owned so uninstall cannot remove it.
		plan.after = before
		return plan, nil
	}
	if hasHyprlandChord(before, lua) {
		return nil, fmt.Errorf("Super+Alt+M is already bound in %s; resolve that binding before using --hyprland-keybind", tilde(e, path))
	}
	if bytes.Contains(before, []byte(begin)) || bytes.Contains(before, []byte(end)) {
		if !force {
			return nil, fmt.Errorf("a Mission Control marker already exists in %s; inspect it or reinstall with --force", tilde(e, path))
		}
		base, found := removeMarkedBinding(before, ManagedAppend{}, begin, end)
		if !found {
			return nil, fmt.Errorf("could not remove the existing Mission Control marker block in %s", tilde(e, path))
		}
		managed.Prefix = separatorFor(base, eol)
		plan.append = managed
		plan.after = appendBlock(base, managed)
		plan.changed = !bytes.Equal(plan.after, before)
		plan.owned = true
		return plan, nil
	}

	plan.after = appendBlock(before, managed)
	plan.changed = true
	plan.owned = true
	return plan, nil
}

func hyprlandBindingBlock(lua bool, eol string) (block, begin, end string) {
	if lua {
		begin = "-- >>> mission-control managed global shortcut >>>"
		end = "-- <<< mission-control managed global shortcut <<<"
		binding := `o.bind("SUPER + ALT + M", "Mission Control", hl.dsp.global("` + hyprlandShortcutAction + `"))`
		return strings.Join([]string{begin, binding, end}, eol), begin, end
	}
	begin = "# >>> mission-control managed global shortcut >>>"
	end = "# <<< mission-control managed global shortcut <<<"
	binding := "bind = SUPER ALT, M, global, " + hyprlandShortcutAction
	return strings.Join([]string{begin, binding, end}, eol), begin, end
}

func appendSegment(a ManagedAppend) string {
	return a.Prefix + a.Block + a.Suffix
}

func separatorFor(content []byte, eol string) string {
	if len(content) == 0 {
		return ""
	}
	if bytes.HasSuffix(content, []byte(eol)) {
		return eol
	}
	return eol + eol
}

func appendBlock(content []byte, a ManagedAppend) []byte {
	addition := []byte(appendSegment(a))
	return append(append([]byte(nil), content...), addition...)
}

func writeHyprlandConfig(path string, content []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mission-control-bindings-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func removeMarkedBinding(content []byte, a ManagedAppend, begin, end string) ([]byte, bool) {
	start := bytes.Index(content, []byte(begin))
	if start < 0 || bytes.Count(content, []byte(begin)) != 1 || bytes.Count(content, []byte(end)) != 1 {
		return content, false
	}
	endStart := bytes.Index(content[start:], []byte(end))
	if endStart < 0 {
		return content, false
	}
	endIndex := start + endStart + len(end)
	removeStart := start
	if a.Prefix != "" && start >= len(a.Prefix) && bytes.Equal(content[start-len(a.Prefix):start], []byte(a.Prefix)) {
		removeStart -= len(a.Prefix)
	}
	removeEnd := endIndex
	if a.Suffix != "" && len(content)-endIndex >= len(a.Suffix) && bytes.Equal(content[endIndex:endIndex+len(a.Suffix)], []byte(a.Suffix)) {
		removeEnd += len(a.Suffix)
	}
	out := make([]byte, 0, len(content)-(removeEnd-removeStart))
	out = append(out, content[:removeStart]...)
	out = append(out, content[removeEnd:]...)
	return out, true
}

func removeManagedAppend(content []byte, a ManagedAppend, force bool) ([]byte, bool) {
	segment := []byte(appendSegment(a))
	if bytes.Count(content, segment) == 1 {
		return bytes.Replace(content, segment, nil, 1), true
	}
	lines := strings.Split(a.Block, "\n")
	if len(lines) < 2 {
		return content, false
	}
	begin := strings.TrimSuffix(lines[0], "\r")
	end := strings.TrimSuffix(lines[len(lines)-1], "\r")
	if !bytes.Contains(content, []byte(begin)) && !bytes.Contains(content, []byte(end)) && !bytes.Contains(content, []byte(hyprlandShortcutAction)) {
		return content, true
	}
	if !force {
		return content, false
	}
	return removeMarkedBinding(content, a, begin, end)
}

func hasHyprlandChord(content []byte, lua bool) bool {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--") || strings.HasPrefix(line, "#") {
			continue
		}
		normalized := strings.ToUpper(strings.Join(strings.Fields(line), " "))
		if lua && strings.Contains(normalized, "SUPER + ALT + M") {
			return true
		}
		if !lua && (strings.Contains(normalized, "SUPER ALT, M,") ||
			strings.Contains(normalized, "ALT SUPER, M,") ||
			strings.Contains(normalized, "$MAINMOD ALT, M,")) {
			return true
		}
	}
	return false
}
